package s2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
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

func TestAppendSession_CanceledSubmitWaitAcceptsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	writes := make(chan struct{}, 3)
	transport := newTransportSession(stream, &signalWriteCloser{signal: writes})
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{MaxInflightBatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	submit := func(body string) *SubmitFuture {
		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte(body)}}})
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

	if _, err := submit("a").Wait(ctx); err != nil {
		t.Fatal(err)
	}
	awaitWrite()

	// b waits for capacity behind a; canceling its wait withdraws it.
	b := submit("b")
	waitCtx, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelWait()
	if _, err := b.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	// Waiting again returns the withdrawal error rather than hanging.
	again := make(chan error, 1)
	go func() {
		_, err := b.Wait(context.Background())
		again <- err
	}()
	select {
	case err := <-again:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a later wait to return the withdrawal error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a later wait on a withdrawn batch hung")
	}
	c := submit("c")

	transport.acksCh <- &AppendAck{Start: StreamPosition{SeqNum: 0}, End: StreamPosition{SeqNum: 1}, Tail: StreamPosition{SeqNum: 1}}
	ticket, err := c.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awaitWrite()
	transport.acksCh <- &AppendAck{Start: StreamPosition{SeqNum: 1}, End: StreamPosition{SeqNum: 2}, Tail: StreamPosition{SeqNum: 2}}
	ack, err := ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("c was not acked by the next ack, so withdrawn b was sent ahead of it: %v", err)
	}
	if ack.Start.SeqNum != 1 {
		t.Fatalf("expected c at seq_num 1, got %d", ack.Start.SeqNum)
	}
}

func TestSubmitFutureCanceledWaitKeepsSettledResult(t *testing.T) {
	ticketCh := make(chan *BatchSubmitTicket, 1)
	future := &SubmitFuture{ticketCh: ticketCh, errCh: make(chan error, 1)}
	future.state.Store(submitSettled)

	// ctx has ended while the session is still queuing the batch.
	want := &BatchSubmitTicket{}
	time.AfterFunc(50*time.Millisecond, func() { ticketCh <- want })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if ticket, err := future.Wait(canceled); err != nil || ticket != want {
		t.Fatalf("a settled batch must return its ticket, got ticket=%p err=%v", ticket, err)
	}
}

// newFreshRequestSession returns a session and a function that submits a
// batch, expects it on a new request, and acks it there with the given advice.
func newFreshRequestSession(t *testing.T, ctx context.Context, retryCfg *RetryConfig) (*AppendSession, func(seq uint64, advise bool) *transportAppendSession) {
	t.Helper()
	stream := newTestStreamClientForAppend(retryCfg)
	writes := make(chan struct{}, 2)
	transports := make(chan *transportAppendSession, 2)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		transport := newTransportSession(stream, &signalWriteCloser{signal: writes})
		transports <- transport
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatal(err)
	}

	return session, func(seq uint64, advise bool) *transportAppendSession {
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
			t.Fatalf("batch %d was not sent on a new request", seq)
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
		return transport
	}
}

func TestAppendSession_AdvisedHandoverDoesNotWaitForResponseEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, appendOne := newFreshRequestSession(t, ctx, DefaultRetryConfig)

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

func TestAppendSession_IdleRequestEndUsesNoBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*transportAppendSession)
	}{
		{"clean end", func(transport *transportAppendSession) {
			close(transport.errorsCh)
			close(transport.acksCh)
		}},
		{"connection breaks", func(transport *transportAppendSession) {
			transport.reportError(fmt.Errorf("read frame error: %w", io.ErrUnexpectedEOF))
		}},
		{"retryable terminal error", func(transport *transportAppendSession) {
			transport.reportError(&S2Error{Status: 503, Code: "unavailable", Origin: "server"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			session, appendOne := newFreshRequestSession(t, ctx, &RetryConfig{
				MaxAttempts:       1,
				AppendRetryPolicy: AppendRetryPolicyAll,
			})

			tc.end(appendOne(0, false))
			for {
				session.sessionMu.RLock()
				released := session.currentSession == nil
				session.sessionMu.RUnlock()
				if released {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("ended request was not released")
				case <-time.After(time.Millisecond):
				}
			}
			appendOne(1, false)
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAppendSession_ProductiveRequestEndSparesQueuedBatch(t *testing.T) {
	session, transport, _ := newAppendAckDrainSession(t, &RetryConfig{
		MaxAttempts:       1,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}, 0)
	transport.ackReceived.Store(true)
	entry, err := session.enqueueReservedEntry(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}}, 1)
	if err != nil {
		t.Fatal(err)
	}

	session.sessionMu.Lock()
	session.currentSession = nil
	session.failuresPending.Add(1)
	session.sessionMu.Unlock()
	session.handleClaimedSessionError(transport, makeConnectionLostError(nil, "test"))

	if atomic.LoadInt32(&entry.completed) != 0 {
		t.Fatal("a batch queued but never sent was failed when an acked request ended")
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

func TestAppendSession_CleanEndWithUnackedIsConnectionLost(t *testing.T) {
	session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
		MaxAttempts:       2,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}, 1)
	close(transport.errorsCh)
	close(transport.acksCh)
	session.readAcks(transport)

	select {
	case result := <-entries[0].resultCh:
		t.Fatalf("unacked batch should be retried, got %+v", result)
	default:
	}
	if session.currentSession != nil || session.currentAttempt != 1 || session.retryAt.IsZero() {
		t.Fatal("a clean end with an unacked batch was not retried as a lost connection")
	}
}

// adviceServerWriter simulates a server that acknowledges every frame actually
// written to the request stream (marking the first with reconnect advice) and
// ends the response cleanly as soon as the request stream is closed.
type adviceServerWriter struct {
	transport *transportAppendSession
	mu        sync.Mutex
	written   int
	closed    bool
}

func (w *adviceServerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	seq := uint64(w.written)
	w.written++
	w.mu.Unlock()

	if seq == 0 {
		w.transport.reconnectAdvised.Store(true)
	}
	w.transport.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: seq},
		End:   StreamPosition{SeqNum: seq + 1},
		Tail:  StreamPosition{SeqNum: seq + 1},
	}
	return len(p), nil
}

func (w *adviceServerWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()

	close(w.transport.errorsCh)
	close(w.transport.acksCh)
	return nil
}

func TestAppendSession_AdviceCleanEndDuringMarshalDoesNotFailQueuedBatch(t *testing.T) {
	session, transport, _ := newAppendAckDrainSession(t, &RetryConfig{
		MaxAttempts:       1,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}, 0)
	// Compress a large incompressible body on the second batch so marshaling
	// takes long enough to overlap with readAcks processing the first ACK and
	// half-closing the request.
	transport.streamClient.basinClient.compression = CompressionGzip
	writer := &adviceServerWriter{transport: transport}
	transport.requestWriter = writer

	first, err := session.enqueueReservedEntry(&AppendInput{
		Records: []AppendRecord{{Body: []byte("first")}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 512*1024)
	for i := range big {
		big[i] = byte(i*31 + i/251)
	}
	second, err := session.enqueueReservedEntry(&AppendInput{
		Records: []AppendRecord{{Body: big}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	readDone := make(chan struct{})
	go func() {
		session.readAcks(transport)
		close(readDone)
	}()

	session.submitInflightBatches()

	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("readAcks did not finish")
	}

	select {
	case res := <-first.resultCh:
		if res == nil || res.err != nil {
			t.Fatalf("expected first batch to succeed, got %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("first batch did not receive ACK")
	}

	select {
	case res := <-second.resultCh:
		if res != nil && res.err != nil {
			t.Fatalf("second batch failed during advised handover: %v", res.err)
		}
	default:
	}

	if session.closed || session.currentAttempt != 0 {
		t.Fatalf("advised clean end failed or retried the session (closed=%v attempt=%d)", session.closed, session.currentAttempt)
	}
}

func TestTicketsReturnTheSameOutcomeToEveryAwait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ack := &AppendAck{End: StreamPosition{SeqNum: 1}}
	batchCh := make(chan *inflightResult, 1)
	batchCh <- &inflightResult{ack: ack}
	close(batchCh)
	batch := &BatchSubmitTicket{ackCh: batchCh}
	recordCh := make(chan *producerOutcome, 1)
	recordCh <- &producerOutcome{indexedAck: &IndexedAppendAck{batchAck: ack}}
	close(recordCh)
	record := &RecordSubmitTicket{ackCh: recordCh}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if got, err := batch.Ack(ctx); err != nil || got != ack {
				t.Errorf("batch ticket: got ack=%v err=%v", got, err)
			}
		}()
		go func() {
			defer wg.Done()
			if got, err := record.Ack(ctx); err != nil || got.BatchAppendAck() != ack {
				t.Errorf("record ticket: got ack=%v err=%v", got, err)
			}
		}()
	}
	wg.Wait()
}

func TestTicketCanceledAwaitLeavesOutcomeForLaterAwaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	want := errors.New("append failed")

	// A canceled pending await neither consumes nor settles the outcome, so a
	// later await gets what the canceled one would have (spec A6, T8).
	check := func(t *testing.T, await func(context.Context) error, deliver func()) {
		t.Helper()
		canceled, cancelAwait := context.WithCancel(ctx)
		pending := make(chan error, 1)
		go func() { pending <- await(canceled) }()
		cancelAwait()
		if err := <-pending; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected the canceled await to return context.Canceled, got %v", err)
		}
		deliver()
		for range 2 {
			if err := await(ctx); !errors.Is(err, want) {
				t.Fatalf("expected the delivered error, got %v", err)
			}
		}
	}

	t.Run("batch", func(t *testing.T) {
		ch := make(chan *inflightResult, 1)
		ticket := &BatchSubmitTicket{ackCh: ch}
		check(t, func(ctx context.Context) error { _, err := ticket.Ack(ctx); return err },
			func() { ch <- &inflightResult{err: want}; close(ch) })
	})
	t.Run("record", func(t *testing.T) {
		ch := make(chan *producerOutcome, 1)
		ticket := &RecordSubmitTicket{ackCh: ch}
		check(t, func(ctx context.Context) error { _, err := ticket.Ack(ctx); return err },
			func() { ch <- &producerOutcome{err: want}; close(ch) })
	})
}
