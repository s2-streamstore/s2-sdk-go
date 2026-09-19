package s2

import (
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
				if session.currentAttempt != 1 || session.advisedReconnects.count != 0 || len(session.inflightQueue) != 1 {
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
		{"terminal error after advice", false, "unavailable"},
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
				if tc.terminalCode == "unavailable" {
					if session.currentSession != nil || session.currentAttempt != 1 || session.advisedReconnects.count != 0 {
						t.Fatal("terminal error did not take precedence over buffered reconnect advice")
					}
				} else if tc.decline {
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
