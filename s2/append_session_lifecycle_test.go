package s2

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestAppendSession_ReadAcksCleanEnd(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		name := "live ACKs"
		if buffered {
			name = "buffered ACKs at EOF"
		}
		t.Run(name, func(t *testing.T) {
			const count = appendAckChannelBuffer
			session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
				MaxAttempts:       1,
				AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
			}, count)
			done := make(chan struct{})
			consume := func() {
				session.readAcks(transport)
				close(done)
			}
			if !buffered {
				go consume()
			}
			for i := range count {
				transport.acksCh <- &AppendAck{
					Start: StreamPosition{SeqNum: uint64(i)},
					End:   StreamPosition{SeqNum: uint64(i + 1)},
					Tail:  StreamPosition{SeqNum: uint64(count)},
				}
			}
			if buffered {
				// The server normally ends the response at EOF, without a
				// terminal success frame. Match the reader's channel order.
				close(transport.errorsCh)
				close(transport.acksCh)
				go consume()
			}
			for i, entry := range entries {
				select {
				case result := <-entry.resultCh:
					if result == nil || result.err != nil || result.ack == nil || result.ack.Start.SeqNum != uint64(i) {
						t.Fatalf("batch %d did not get its ACK: %+v", i, result)
					}
				case <-time.After(time.Second):
					t.Fatalf("batch %d waited for EOF to receive its ACK", i)
				}
			}
			if !buffered {
				close(transport.errorsCh)
				close(transport.acksCh)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("ACK reader did not finish at clean EOF")
			}
			if session.closed || session.currentAttempt != 0 || !session.retryAt.IsZero() {
				t.Fatal("clean EOF failed the session or scheduled a retry")
			}
			if len(session.inflightQueue) != 0 || len(session.sessionRefs) != 0 || transport.pendingWrites != 0 || session.capacity.curItems != 0 {
				t.Fatal("clean EOF left acknowledged appends in flight")
			}
		})
	}
}

func TestAppendSession_ReadAcksAcceptedReconnect(t *testing.T) {
	for _, terminalError := range []bool{false, true} {
		name := "clean EOF"
		if terminalError {
			name = "terminal error after advice"
		}
		t.Run(name, func(t *testing.T) {
			count := 1
			if terminalError {
				count++
			}
			session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
				MaxAttempts:       3,
				AppendRetryPolicy: AppendRetryPolicyAll,
			}, count)
			writer := &blockingClosedPipeWriter{closed: make(chan struct{})}
			transport.requestWriter = writer
			transport.reconnectAdvised.Store(true)
			done := make(chan struct{})
			go func() {
				session.readAcks(transport)
				close(done)
			}()
			transport.acksCh <- &AppendAck{
				Start: StreamPosition{SeqNum: 0},
				End:   StreamPosition{SeqNum: 1},
				Tail:  StreamPosition{SeqNum: 1},
			}
			// As in the Rust SDK, accepting advice must half-close the
			// request before waiting for the server to finish its response.
			select {
			case <-writer.closed:
			case <-time.After(time.Second):
				t.Fatal("reconnect advice did not half-close the request")
			}
			if terminalError {
				transport.reportError(&S2Error{Status: 503, Code: "unavailable", Origin: "server"})
			}
			close(transport.errorsCh)
			close(transport.acksCh)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("ACK reader did not finish after advice")
			}
			result := <-entries[0].resultCh
			if result == nil || result.err != nil || result.ack == nil {
				t.Fatalf("advised ACK was not delivered: %+v", result)
			}
			if session.closed || session.currentSession != nil {
				t.Fatal("session was not ready for a new transport")
			}
			if terminalError {
				if session.currentAttempt != 1 || len(session.inflightQueue) != 1 {
					t.Fatal("terminal error was mistaken for a clean advised reconnect")
				}
			} else if session.currentAttempt != 0 || session.advisedReconnects.count != 1 || len(session.inflightQueue) != 0 {
				t.Fatal("clean advised reconnect consumed a retry or left appends in flight")
			}
		})
	}
}

func TestAppendSession_ReadAcksBufferedReconnect(t *testing.T) {
	for _, tc := range []struct {
		name         string
		decline      bool
		terminalCode string
	}{
		{"accepted at clean EOF", false, ""},
		{"declined at clean EOF", true, ""},
		{"terminal error after advice acked", false, "unavailable"},
		{"server draining after advice", false, "server_draining"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Sample both the ACK and closed-errors select branches.
			for range 32 {
				const count = appendAckChannelBuffer
				session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
					MaxAttempts:       3,
					AppendRetryPolicy: AppendRetryPolicyAll,
				}, count)
				if tc.decline {
					session.advisedReconnects.record(time.Now())
				}
				transport.reconnectAdvised.Store(true)
				for i := range count {
					transport.acksCh <- &AppendAck{
						Start: StreamPosition{SeqNum: uint64(i)},
						End:   StreamPosition{SeqNum: uint64(i + 1)},
						Tail:  StreamPosition{SeqNum: count},
					}
				}
				if tc.terminalCode != "" {
					transport.reportError(&S2Error{Status: 503, Code: tc.terminalCode, Origin: "server"})
				}
				close(transport.errorsCh)
				close(transport.acksCh)
				transport.Close()

				session.readAcks(transport)

				for i, entry := range entries {
					select {
					case result := <-entry.resultCh:
						if result == nil || result.err != nil || result.ack == nil || result.ack.Start.SeqNum != uint64(i) {
							t.Fatalf("buffered batch %d did not get its ACK: %+v", i, result)
						}
					default:
						t.Fatalf("buffered batch %d was not completed", i)
					}
				}
				// Once all its batches are acked, the advised request is left
				// behind, so a later error on it does not count as a retry.
				if tc.decline {
					if transport.holdInputsForReconnect() || !transport.reconnectDeclined.Load() || session.currentAttempt != 0 || session.advisedReconnects.count != 1 {
						t.Fatal("declined buffered advice blocked later submissions or consumed a retry")
					}
				} else if session.currentSession != nil || session.currentAttempt != 0 || session.advisedReconnects.count != 1 {
					t.Fatal("accepted buffered advice did not release the closed transport for reconnect")
				}
			}
		})
	}
}

func TestAppendSession_SubmitOrderSurvivesBackpressure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	writes := make(chan struct{}, 3)
	transport := newTransportSession(stream, &signalWriteCloser{signal: writes})
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{MaxInflightBytes: minInflightBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	submit := func(size int) *SubmitFuture {
		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: make([]byte, size)}}})
		if err != nil {
			t.Fatal(err)
		}
		return future
	}
	awaitWrite := func() {
		select {
		case <-writes:
		case <-ctx.Done():
			t.Fatal("batch was not sent")
		}
	}
	ack := func(seq uint64) {
		transport.acksCh <- &AppendAck{
			Start: StreamPosition{SeqNum: seq},
			End:   StreamPosition{SeqNum: seq + 1},
			Tail:  StreamPosition{SeqNum: seq + 1},
		}
	}

	if _, err := submit(700_000).Wait(ctx); err != nil {
		t.Fatal(err)
	}
	awaitWrite()
	// b waits for capacity; c would fit alongside the first batch.
	b, c := submit(400_000), submit(100_000)
	ack(0)
	tickets := make([]*BatchSubmitTicket, 0, 2)
	for _, future := range []*SubmitFuture{b, c} {
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}
	awaitWrite()
	awaitWrite()
	ack(1)
	ack(2)
	for i, ticket := range tickets {
		got, err := ticket.Ack(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.Start.SeqNum != uint64(i+1) {
			t.Fatalf("batch %d was appended at %d, out of submission order", i+1, got.Start.SeqNum)
		}
	}
}

func TestAppendSession_AdvisedHandoverDoesNotWaitForResponseEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	writes := make(chan struct{}, 2)
	transports := make(chan *transportAppendSession, 2)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		transport := newTransportSession(stream, &signalWriteCloser{signal: writes})
		transports <- transport
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	appendOne := func(seq uint64, advise bool) {
		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var transport *transportAppendSession
		select {
		case transport = <-transports:
		case <-ctx.Done():
			t.Fatalf("batch %d was held on the advised request", seq)
		}
		select {
		case <-writes:
		case <-ctx.Done():
			t.Fatalf("batch %d was not sent", seq)
		}
		transport.reconnectAdvised.Store(advise)
		transport.acksCh <- &AppendAck{
			Start: StreamPosition{SeqNum: seq},
			End:   StreamPosition{SeqNum: seq + 1},
			Tail:  StreamPosition{SeqNum: seq + 1},
		}
		if _, err := ticket.Ack(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// The advised response stays open and silent after acking everything.
	appendOne(0, true)
	appendOne(1, false)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if session.currentAttempt != 0 {
		t.Fatal("advised handover consumed a retry")
	}
}

type gatedWriter struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	closed  bool
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (w *gatedWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

func TestAppendSession_AdviceDuringWriteIsNotAFailure(t *testing.T) {
	session, transport, _ := newAppendAckDrainSession(t, &RetryConfig{
		MaxAttempts:       1,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}, 0)
	writer := &gatedWriter{started: make(chan struct{}), release: make(chan struct{})}
	transport.requestWriter = writer
	entries := make([]*inflightEntry, 2)
	for i := range entries {
		entry, err := session.enqueueReservedEntry(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = entry
	}

	pumped := make(chan struct{})
	go func() {
		session.submitInflightBatches()
		close(pumped)
	}()
	<-writer.started
	// What readAcks does on accepting advice, while the first batch is
	// still being written.
	transport.reconnectAdvised.Store(true)
	transport.halfClose()
	close(writer.release)
	<-pumped

	if session.closed || session.currentAttempt != 0 || session.currentSession != transport {
		t.Fatal("half-closing for reconnect advice was handled as a failure")
	}
	if !entries[0].wasSentOnSessionLocked(transport) || entries[1].wasSentOnSessionLocked(transport) || !writer.closed {
		t.Fatal("the in-progress batch should finish sending before the request is half-closed")
	}
}
