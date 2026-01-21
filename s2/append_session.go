package s2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	appendPumpTickInterval = 10 * time.Millisecond
	appendAckChannelBuffer = 20
)

// AppendSession provides ordered, pipelined appends with automatic retries.
type AppendSession struct {
	streamClient *StreamClient
	options      *AppendSessionOptions

	inflightQueue []*inflightEntry
	inflightMu    sync.RWMutex
	capacity      *capacityTracker

	currentSession *transportAppendSession
	sessionMu      sync.RWMutex

	pumpCtx    context.Context
	pumpCancel context.CancelFunc
	pumpDone   chan struct{}

	readWG sync.WaitGroup

	closed    bool
	closedMu  sync.RWMutex
	closeOnce sync.Once

	lastAckedPosition *AppendAck
	stateMu           sync.RWMutex

	wakeup         chan struct{}
	currentAttempt int
	retryAt        time.Time
}

// Creates an append session that guarantees ordering of submissions.
// Use this to coordinate high-throughput, sequential appends with backpressure.
func (s *StreamClient) AppendSession(ctx context.Context, opts *AppendSessionOptions) (*AppendSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	opts = applyAppendSessionDefaults(opts, s.basinClient.retryConfig)
	pumpCtx, pumpCancel := context.WithCancel(ctx)

	session := &AppendSession{
		streamClient: s,
		options:      opts,
		capacity:     newCapacityTracker(int64(opts.MaxInflightBytes), int(opts.MaxInflightBatches)),
		pumpCtx:      pumpCtx,
		pumpCancel:   pumpCancel,
		pumpDone:     make(chan struct{}),
		wakeup:       make(chan struct{}, 1),
	}

	go session.runPump()

	return session, nil
}

// Submit an append request.
// Returns a [SubmitFuture] that resolves to a submit ticket once the batch is enqueued (has capacity).
// Call ticket.Wait() to get a [SubmitFuture] for the [AppendAck] once the batch is durable.
// This method applies backpressure and will block if capacity limits are reached.
func (r *AppendSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	prepared, size, err := prepareAppendInput(input)
	if err != nil {
		return nil, err
	}

	return r.createSubmitFuture(prepared, size), nil
}

func (r *AppendSession) createSubmitFuture(input *AppendInput, size int64) *SubmitFuture {
	ticketCh := make(chan *BatchSubmitTicket, 1)
	errCh := make(chan error, 1)

	go func() {
		if err := r.capacity.reserve(r.pumpCtx, size); err != nil {
			errCh <- err
			return
		}

		entry := r.enqueueEntry(input, size)
		ticketCh <- &BatchSubmitTicket{ackCh: entry.resultCh}
	}()

	return &SubmitFuture{ticketCh: ticketCh, errCh: errCh}
}

// Closes the session.
func (r *AppendSession) Close() error {
	var closeErr error

	r.closeOnce.Do(func() {
		r.closedMu.Lock()
		if !r.closed {
			r.closed = true
			r.capacity.Close()
		}
		r.closedMu.Unlock()

		r.pumpCancel()
		<-r.pumpDone

		r.readWG.Wait()

		r.sessionMu.RLock()
		if r.currentSession != nil {
			closeErr = r.currentSession.Close()
		}
		r.sessionMu.RUnlock()
	})

	return closeErr
}

// Returns the last acknowledged position, if any.
func (r *AppendSession) LastAckedPosition() *AppendAck {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.lastAckedPosition
}

// adds the entry to the inflight queue
func (r *AppendSession) enqueueEntry(input *AppendInput, size int64) *inflightEntry {
	entry := &inflightEntry{
		input:          input,
		expectedCount:  len(input.Records),
		meteredBytes:   size,
		requestTimeout: r.streamClient.basinClient.requestTimeout,
		resultCh:       make(chan *inflightResult, 1),
	}

	r.inflightMu.Lock()
	r.inflightQueue = append(r.inflightQueue, entry)
	r.inflightMu.Unlock()

	r.wakeupPump()

	return entry
}

// the main session loop that processes the inflight queue
func (r *AppendSession) runPump() {
	defer close(r.pumpDone)

	ticker := time.NewTicker(appendPumpTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.pumpCtx.Done():
			r.failAllInflight(ErrSessionClosed)
			return

		case <-r.wakeup:
			r.processInflightQueue()

		case <-ticker.C:
			r.processInflightQueue()
			r.checkTimeouts()
		}
	}
}

func (r *AppendSession) wakeupPump() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

func (r *AppendSession) processInflightQueue() {
	select {
	case <-r.pumpCtx.Done():
		return
	default:
	}

	r.stateMu.RLock()
	retryAt := r.retryAt
	r.stateMu.RUnlock()
	if !retryAt.IsZero() && time.Now().Before(retryAt) {
		return
	}
	r.stateMu.Lock()
	r.retryAt = time.Time{}
	r.stateMu.Unlock()

	r.inflightMu.RLock()
	queueLen := len(r.inflightQueue)
	r.inflightMu.RUnlock()

	if queueLen == 0 {
		return
	}

	logDebug(r.streamClient.logger, "append pump processing inflight",
		"stream", string(r.streamClient.name),
		"queue_len", queueLen)

	if err := r.getSession(); err != nil {
		var s2Err *S2Error
		if errors.As(err, &s2Err) && s2Err.IsRetryable() {
			logError(r.streamClient.logger, "append pump transport start failed, will retry",
				"stream", string(r.streamClient.name),
				"error", err)
			r.handleSessionError(err)
		} else {
			logError(r.streamClient.logger, "append pump transport start failed fatally",
				"stream", string(r.streamClient.name),
				"error", err)
			r.failAllInflight(err)
		}
		return
	}

	r.submitInflightBatches()
}

func (r *AppendSession) getSession() error {
	r.sessionMu.RLock()
	if r.currentSession != nil {
		r.sessionMu.RUnlock()
		return nil
	}
	r.sessionMu.RUnlock()

	r.sessionMu.Lock()
	defer r.sessionMu.Unlock()

	if r.currentSession != nil {
		return nil
	}

	logInfo(r.streamClient.logger, "append pump opening transport session",
		"stream", string(r.streamClient.name))

	session, err := r.streamClient.createAppendSession(r.pumpCtx)
	if err != nil {
		logError(r.streamClient.logger, "append pump failed to open transport session",
			"stream", string(r.streamClient.name),
			"error", err)
		return err
	}

	r.currentSession = session
	r.readWG.Add(1)
	go func() {
		defer r.readWG.Done()
		r.readAcks(session)
	}()

	logInfo(r.streamClient.logger, "append pump transport session ready",
		"stream", string(r.streamClient.name))

	return nil
}

func (r *AppendSession) submitInflightBatches() {
	r.sessionMu.RLock()
	session := r.currentSession
	r.sessionMu.RUnlock()

	if session == nil {
		return
	}

	r.inflightMu.Lock()
	entries := make([]*inflightEntry, len(r.inflightQueue))
	copy(entries, r.inflightQueue)
	for _, entry := range entries {
		if entry.attemptStart.IsZero() {
			entry.attemptStart = time.Now()
		}
	}
	r.inflightMu.Unlock()

	for _, entry := range entries {
		if err := session.appendInput(entry.input); err != nil {
			r.handleSessionError(err)
			return
		}
	}
}

func (r *AppendSession) readAcks(session *transportAppendSession) {
	for {
		select {
		case ack, ok := <-session.acksCh:
			if !ok {
				return
			}
			r.handleAck(session, ack)

		case err, ok := <-session.errorsCh:
			if !ok {
				return
			}
			r.handleSessionError(err)
			return

		case <-r.pumpCtx.Done():
			return
		}
	}
}

func (r *AppendSession) handleAck(session *transportAppendSession, ack *AppendAck) {
	r.sessionMu.RLock()
	current := r.currentSession
	r.sessionMu.RUnlock()
	if current != session {
		return
	}

	r.inflightMu.Lock()

	if len(r.inflightQueue) == 0 {
		r.inflightMu.Unlock()
		return
	}

	entry := r.inflightQueue[0]

	if err := r.validateAckLocked(entry, ack); err != nil {
		r.inflightMu.Unlock()
		logError(r.streamClient.logger, "append session ack invariant violated",
			"stream", string(r.streamClient.name),
			"error", err)
		r.failAllInflight(err)
		return
	}

	if !atomic.CompareAndSwapInt32(&entry.completed, 0, 1) {
		r.inflightMu.Unlock()
		return
	}

	r.inflightQueue = r.inflightQueue[1:]
	r.inflightMu.Unlock()

	r.capacity.release(entry.meteredBytes)

	r.stateMu.Lock()
	r.lastAckedPosition = ack
	r.currentAttempt = 0
	r.stateMu.Unlock()

	select {
	case entry.resultCh <- &inflightResult{ack: ack}:
		close(entry.resultCh)
	default:
	}
}

func (r *AppendSession) validateAckLocked(entry *inflightEntry, ack *AppendAck) error {
	if ack == nil {
		return fmt.Errorf("append ack invariant violated: received nil ack")
	}

	start := ack.Start.SeqNum
	end := ack.End.SeqNum

	if end < start {
		return fmt.Errorf("append ack invariant violated: end seq %d is less than start seq %d", end, start)
	}

	ackCount := end - start
	if ackCount != uint64(entry.expectedCount) {
		return fmt.Errorf("append ack invariant violated: expected %d records, got %d (start=%d, end=%d)",
			entry.expectedCount, ackCount, start, end)
	}

	r.stateMu.RLock()
	last := r.lastAckedPosition
	r.stateMu.RUnlock()

	if last != nil {
		prevEnd := last.End.SeqNum
		if end <= prevEnd {
			return fmt.Errorf("append ack invariant violated: end seq %d is not greater than previous end %d",
				end, prevEnd)
		}
	}

	return nil
}

func (r *AppendSession) handleSessionError(err error) {
	r.closedMu.RLock()
	closed := r.closed
	r.closedMu.RUnlock()
	if closed {
		return
	}

	logError(r.streamClient.logger, "append session transport error",
		"stream", string(r.streamClient.name),
		"error", err)

	if r.requiresIdempotentRetries() {
		r.inflightMu.RLock()
		var head *inflightEntry
		if len(r.inflightQueue) > 0 {
			head = r.inflightQueue[0]
		}
		r.inflightMu.RUnlock()

		if head == nil || !isIdempotentEntry(head) {
			logError(r.streamClient.logger, "append session cannot retry (non-idempotent head entry)",
				"stream", string(r.streamClient.name))
			r.failAllInflight(err)
			return
		}
	}

	r.sessionMu.Lock()
	r.currentSession = nil
	r.sessionMu.Unlock()

	var s2Err *S2Error
	if errors.As(err, &s2Err) && !s2Err.IsRetryable() {
		logError(r.streamClient.logger, "append session error not retryable",
			"stream", string(r.streamClient.name),
			"status", s2Err.Status,
			"code", s2Err.Code)
		r.failAllInflight(err)
		return
	}

	maxAttempts := r.options.RetryConfig.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	r.stateMu.RLock()
	currentAttempt := r.currentAttempt
	r.stateMu.RUnlock()

	if currentAttempt >= maxAttempts-1 {
		logError(r.streamClient.logger, "append session max attempts exhausted",
			"stream", string(r.streamClient.name),
			"attempts", maxAttempts)
		r.failAllInflight(fmt.Errorf("max attempts (%d) exhausted, last error: %v: %w",
			maxAttempts, err, ErrMaxAttemptsExhausted))
		return
	}

	r.stateMu.Lock()
	r.currentAttempt++
	r.stateMu.Unlock()

	r.inflightMu.Lock()
	for _, entry := range r.inflightQueue {
		entry.attemptStart = time.Time{}
	}
	r.inflightMu.Unlock()

	r.scheduleRetry()
}

func (r *AppendSession) failAllInflight(err error) {
	r.inflightMu.Lock()
	queueLen := len(r.inflightQueue)
	r.inflightMu.Unlock()

	logError(r.streamClient.logger, "append session failing all inflight",
		"stream", string(r.streamClient.name),
		"queue_len", queueLen,
		"error", err)

	r.inflightMu.Lock()
	defer r.inflightMu.Unlock()
	r.failAllInflightLocked(err)
}

func (r *AppendSession) failAllInflightLocked(err error) {
	for _, entry := range r.inflightQueue {
		if atomic.CompareAndSwapInt32(&entry.completed, 0, 1) {
			select {
			case entry.resultCh <- &inflightResult{err: err}:
				close(entry.resultCh)
			default:
			}
		}
		r.capacity.release(entry.meteredBytes)
	}

	r.inflightQueue = nil

	r.closedMu.Lock()
	if !r.closed {
		r.closed = true
		r.capacity.Close()
	}
	r.closedMu.Unlock()
	r.pumpCancel()
}

func (r *AppendSession) scheduleRetry() {
	r.stateMu.RLock()
	attempt := r.currentAttempt
	r.stateMu.RUnlock()

	delay := calculateRetryBackoff(r.options.RetryConfig, attempt)

	logInfo(r.streamClient.logger, "append session scheduling retry",
		"stream", string(r.streamClient.name),
		"attempt", attempt,
		"delay", delay)

	if delay > 0 {
		r.stateMu.Lock()
		if r.retryAt.IsZero() {
			r.retryAt = time.Now().Add(delay)
		}
		r.stateMu.Unlock()
	}
}

func (r *AppendSession) checkTimeouts() {
	select {
	case <-r.pumpCtx.Done():
		return
	default:
	}

	r.inflightMu.RLock()
	var timedOut bool
	var attemptStart time.Time
	var requestTimeout time.Duration
	if len(r.inflightQueue) > 0 {
		head := r.inflightQueue[0]
		attemptStart = head.attemptStart
		requestTimeout = head.requestTimeout
		timedOut = requestTimeout > 0 && !attemptStart.IsZero() && time.Since(attemptStart) > requestTimeout
	}
	r.inflightMu.RUnlock()

	if timedOut {
		elapsed := time.Since(attemptStart)
		r.stateMu.RLock()
		attempt := r.currentAttempt
		r.stateMu.RUnlock()
		err := fmt.Errorf("append request timed out after %v (attempt %d): %w",
			elapsed, attempt, ErrTimeout)
		logError(r.streamClient.logger, "append request timeout",
			"stream", string(r.streamClient.name),
			"elapsed", elapsed,
			"attempt", attempt)
		r.failAllInflight(err)
	}
}

func (r *AppendSession) requiresIdempotentRetries() bool {
	return r.options.RetryConfig != nil && r.options.RetryConfig.AppendRetryPolicy == AppendRetryPolicyNoSideEffects
}

type inflightEntry struct {
	input          *AppendInput
	expectedCount  int
	meteredBytes   int64
	attemptStart   time.Time
	requestTimeout time.Duration
	resultCh       chan *inflightResult
	completed      int32
}

type inflightResult struct {
	ack *AppendAck
	err error
}

func isIdempotentEntry(entry *inflightEntry) bool {
	return entry != nil && entry.input != nil && entry.input.MatchSeqNum != nil
}
