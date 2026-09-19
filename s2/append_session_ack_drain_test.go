package s2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

func TestAppendSession_ReadAcksDrainsBeforeError(t *testing.T) {
	for _, tc := range []struct {
		name        string
		policy      AppendRetryPolicy
		status      int
		code        string
		maxAttempts int
		unacked     int
		wantRetry   bool
	}{
		{"retry unacknowledged only", AppendRetryPolicyAll, 503, "unavailable", 3, 1, true},
		{"all acknowledged", AppendRetryPolicyAll, 503, "unavailable", 3, 0, true},
		{"no side effects", AppendRetryPolicyNoSideEffects, 503, "unavailable", 3, 1, false},
		{"no side effects all acknowledged", AppendRetryPolicyNoSideEffects, 503, "unavailable", 3, 0, true},
		{"no side effects safe error", AppendRetryPolicyNoSideEffects, 429, "rate_limited", 3, 1, true},
		{"nonretryable error", AppendRetryPolicyAll, 400, "validation", 3, 1, false},
		{"retry exhausted", AppendRetryPolicyAll, 503, "unavailable", 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const ackCount = appendAckChannelBuffer
			session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
				AppendRetryPolicy: tc.policy,
				MaxAttempts:       tc.maxAttempts,
			}, ackCount+tc.unacked)

			// Decode an actual response before consuming it, as when the
			// transport reader runs ahead of the session's ACK goroutine.
			var body bytes.Buffer
			for i := range ackCount {
				data, err := proto.Marshal(&pb.AppendAck{
					Start: &pb.StreamPosition{SeqNum: uint64(i)},
					End:   &pb.StreamPosition{SeqNum: uint64(i + 1)},
					Tail:  &pb.StreamPosition{SeqNum: uint64(ackCount)},
				})
				if err != nil {
					t.Fatal(err)
				}
				body.Write(framing.CreateFrame(data, false, framing.CompressionNone))
			}
			body.Write(framing.CreateFrameWithStatus(
				[]byte(fmt.Sprintf(`{"code":%q,"message":"response failed"}`, tc.code)),
				true, framing.CompressionNone, tc.status,
			))
			transport.readAcksLoop(&http.Response{Body: io.NopCloser(&body)})
			if len(transport.acksCh) != ackCount || len(transport.errorsCh) != 1 {
				t.Fatal("response did not buffer all ACKs followed by the terminal error")
			}

			session.readAcks(transport)

			if tc.wantRetry {
				// Exercise the next send, including the non-idempotent input
				// path. Already acknowledged records must never be replayed.
				writes := make(chan struct{}, len(entries))
				retryTransport := newTransportSession(session.streamClient, &signalWriteCloser{signal: writes})
				t.Cleanup(func() { retryTransport.Close() })
				if session.currentSession != nil {
					t.Fatal("terminal error did not clear the failed session")
				}
				session.currentSession = retryTransport
				session.submitInflightBatches()
				if got := len(writes); got != tc.unacked {
					t.Fatalf("retry wrote %d batches, want only %d unacknowledged batches", got, tc.unacked)
				}
			}

			for i, entry := range entries[:ackCount] {
				select {
				case result := <-entry.resultCh:
					if result == nil || result.err != nil || result.ack == nil {
						t.Fatalf("acknowledged batch %d did not succeed: %+v", i, result)
					}
					if result.ack.Start.SeqNum != uint64(i) || result.ack.End.SeqNum != uint64(i+1) {
						t.Fatalf("batch %d got wrong ACK: %+v", i, result.ack)
					}
				default:
					t.Fatalf("acknowledged batch %d was left pending", i)
				}
			}
			if got := session.LastAckedPosition(); got == nil || got.End.SeqNum != ackCount {
				t.Fatalf("last acknowledged position = %+v, want end %d", got, ackCount)
			}
			if got := transport.pendingWrites; got != tc.unacked {
				t.Fatalf("pending writes = %d, want %d", got, tc.unacked)
			}

			remaining := 0
			if tc.wantRetry {
				remaining = tc.unacked
				if session.closed {
					t.Fatal("retryable session was closed")
				}
			} else {
				select {
				case result := <-entries[ackCount].resultCh:
					if result == nil || result.err == nil {
						t.Fatalf("unacknowledged batch did not fail: %+v", result)
					}
					if tc.maxAttempts == 1 {
						if !errors.Is(result.err, ErrMaxAttemptsExhausted) {
							t.Fatalf("expected exhausted retries, got %v", result.err)
						}
					} else if !errors.Is(result.err, transport.terminalError()) {
						t.Fatalf("expected terminal error %v, got %v", transport.terminalError(), result.err)
					}
				default:
					t.Fatal("unacknowledged batch was left pending after terminal failure")
				}
			}
			if len(session.inflightQueue) != remaining || session.capacity.curItems != remaining || session.capacity.curBytes != int64(remaining) {
				t.Fatalf("queue/capacity not released: queue=%d items=%d bytes=%d, want %d",
					len(session.inflightQueue), session.capacity.curItems, session.capacity.curBytes, remaining)
			}
		})
	}
}

func TestAppendSession_ReadAcksHandlesErrorAfterAckChannelCloses(t *testing.T) {
	// Exercise both select choices when the ACK channel is already empty
	// and closed but a terminal error is still buffered.
	for range 32 {
		session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
			AppendRetryPolicy: AppendRetryPolicyAll,
			MaxAttempts:       3,
		}, 1)
		terminalErr := &S2Error{Status: 400, Message: "bad request"}
		transport.reportError(terminalErr)
		close(transport.errorsCh)
		close(transport.acksCh)

		session.readAcks(transport)

		select {
		case result := <-entries[0].resultCh:
			if result == nil || !errors.Is(result.err, terminalErr) {
				t.Fatalf("expected terminal error %v, got %+v", terminalErr, result)
			}
		default:
			t.Fatal("closed ACK channel hid the buffered terminal error")
		}
	}
}

func TestAppendSession_ReadAcksErrorDoesNotWaitForChannelClose(t *testing.T) {
	session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
		AppendRetryPolicy: AppendRetryPolicyAll,
		MaxAttempts:       3,
	}, 1)
	terminalErr := &S2Error{Status: 400, Message: "connection rejected"}
	transport.reportError(terminalErr)
	// connectAndRead can report an error without ever starting readAcksLoop,
	// so neither channel is closed in this case.
	done := make(chan struct{})
	go func() {
		session.readAcks(transport)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(transport.acksCh)
		<-done
		t.Fatal("error handling waited for the ACK channel to close")
	}
	select {
	case result := <-entries[0].resultCh:
		if result == nil || !errors.Is(result.err, terminalErr) {
			t.Fatalf("expected terminal error %v, got %+v", terminalErr, result)
		}
	default:
		t.Fatal("unacknowledged batch did not fail")
	}
}

func newAppendAckDrainSession(t *testing.T, retryCfg *RetryConfig, count int) (*AppendSession, *transportAppendSession, []*inflightEntry) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := newTestStreamClientForAppend(retryCfg)
	transport := newTransportSession(stream, &signalWriteCloser{})
	transport.acksCh = make(chan *AppendAck, appendAckChannelBuffer)
	t.Cleanup(func() { transport.Close() })
	session := &AppendSession{
		streamClient:   stream,
		options:        &AppendSessionOptions{RetryConfig: retryCfg},
		capacity:       newCapacityTracker(1024, count),
		sessionRefs:    make(map[*transportAppendSession]int),
		currentSession: transport,
		pumpCtx:        ctx,
		pumpCancel:     cancel,
		closeDone:      make(chan struct{}),
		wakeup:         make(chan struct{}, 1),
	}
	entries := make([]*inflightEntry, count)
	for i := range count {
		if err := session.capacity.reserve(ctx, 1); err != nil {
			t.Fatal(err)
		}
		entry, err := session.enqueueReservedEntry(&AppendInput{
			Records: []AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = entry
	}
	// Drive sends and ACK consumption separately so the response can be
	// fully buffered without relying on goroutine scheduling or sleeps.
	session.submitInflightBatches()
	return session, transport, entries
}
