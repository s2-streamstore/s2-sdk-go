package s2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// Reported by CaughtUpFuture.Wait when the session terminates before reaching
// the tail, whether due to a fatal error or a limit (`count`, `bytes`, or
// `until`) being met. Inspect ReadSession.Err for the underlying error, if
// any.
var ErrSessionEndedBeforeCaughtUp = errors.New("read session ended before catching up")

// Tracks whether the session has caught up to the stream's tail.
// Guards a nil-able tail (non-nil while caught up) and an ended latch, and
// wakes WaitCaughtUp waiters on transitions they care about.
type caughtUpState struct {
	mu    sync.Mutex
	tail  *StreamPosition
	ended bool
	// Closed to wake waiters on a transition to caught up or ended, then
	// replaced. Created lazily by the first waiter.
	changed chan struct{}
}

func (c *caughtUpState) isCaughtUp() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tail != nil
}

func (c *caughtUpState) setCaughtUp(tail StreamPosition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.tail = &tail
	c.notifyLocked()
}

func (c *caughtUpState) setBehind() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	// Waiters only care about transitions to caught up or ended; no notify.
	c.tail = nil
}

// Latches the terminal state. A forced end (fatal error) also clears the
// caught-up signal; a clean end (limits met, session closed) preserves it, so
// a session that ended at the tail keeps reporting caught up.
func (c *caughtUpState) end(force bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	if force {
		c.tail = nil
	}
	if c.tail != nil {
		return
	}
	c.ended = true
	c.notifyLocked()
}

func (c *caughtUpState) notifyLocked() {
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
}

func (c *caughtUpState) wait(ctx context.Context) (StreamPosition, error) {
	for {
		c.mu.Lock()
		if c.tail != nil {
			tail := *c.tail
			c.mu.Unlock()
			return tail, nil
		}
		if c.ended {
			c.mu.Unlock()
			return StreamPosition{}, ErrSessionEndedBeforeCaughtUp
		}
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return StreamPosition{}, ctx.Err()
		}
	}
}

type ReadSession struct {
	reader  *streamReader
	pending []SequencedRecord
	// Non-nil while the pending records belong to a delivery that reaches the
	// stream's tail; consumed into the caught-up signal when the last of them
	// is yielded.
	pendingCaughtUpTail *StreamPosition
	current             SequencedRecord
	err                 error
	closed              atomic.Bool
	caughtUp            caughtUpState
}

// Opens a streaming read session.
// Call Close when done consuming records.
func (s *StreamClient) ReadSession(ctx context.Context, opts *ReadOptions) (*ReadSession, error) {
	reader, err := s.newStreamReader(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &ReadSession{reader: reader}, nil
}

// Blocks until a record is available, the context is done, or the
// session encounters a fatal error. When it returns true, Record() yields
// the fetched record. When it returns false, call Err().
func (s *ReadSession) Next() bool {
	if s == nil || s.reader == nil || s.err != nil || s.closed.Load() {
		return false
	}

	if s.yieldPending() {
		return true
	}

	for {
		select {
		case delivery, ok := <-s.reader.Records():
			if !ok {
				// Records closed - drain any pending error before exiting
				select {
				case err := <-s.reader.Errors():
					if err != nil {
						s.err = err
					}
				default:
				}
				s.caughtUp.end(s.err != nil)
				s.Close()
				return false
			}
			s.stashDelivery(delivery)
			if s.yieldPending() {
				return true
			}

		case err, ok := <-s.reader.Errors():
			if !ok {
				select {
				case delivery, ok := <-s.reader.Records():
					if !ok {
						s.caughtUp.end(false)
						s.Close()
						return false
					}
					s.stashDelivery(delivery)
					if s.yieldPending() {
						return true
					}
				default:
					s.caughtUp.end(false)
					s.Close()
					return false
				}
			}
			if err != nil {
				s.err = err
				s.caughtUp.end(true)
				s.Close()
				return false
			}
		}
	}
}

func (s *ReadSession) stashDelivery(delivery readDelivery) {
	s.pending = delivery.records
	s.pendingCaughtUpTail = delivery.caughtUpTail
	if delivery.caughtUpTail != nil && len(delivery.records) == 0 {
		// A caught-up signal with no accompanying records: a heartbeat, or a
		// batch whose records at the tail were all filtered out.
		s.caughtUp.setCaughtUp(*delivery.caughtUpTail)
		s.pendingCaughtUpTail = nil
		return
	}
	s.caughtUp.setBehind()
}

func (s *ReadSession) yieldPending() bool {
	if len(s.pending) == 0 {
		return false
	}
	s.current = s.pending[0]
	s.pending[0] = SequencedRecord{} // release references so consumed records can be collected
	s.pending = s.pending[1:]
	if len(s.pending) == 0 && s.pendingCaughtUpTail != nil {
		s.caughtUp.setCaughtUp(*s.pendingCaughtUpTail)
		s.pendingCaughtUpTail = nil
	}
	return true
}

// Returns the most recent record fetched by Next.
func (s *ReadSession) Record() SequencedRecord {
	return s.current
}

// Reports the terminal error (if any) that caused Next to stop.
func (s *ReadSession) Err() error {
	return s.err
}

// Stops the session.
func (s *ReadSession) Close() error {
	if s == nil || s.reader == nil {
		return nil
	}
	if s.closed.Swap(true) {
		return nil
	}
	s.caughtUp.end(false)
	return s.reader.Close()
}

// Reports whether the session is at the live tail: every record that existed
// as of the last server report has been fetched via Next. The signal is
// derived from server reports already on the wire:
//
//   - A heartbeat (empty batch carrying the tail) marks the session as caught
//     up. The first one marks the backlog-to-live transition, periodic ones
//     confirm idle-at-tail.
//   - A batch carrying the tail marks the session as caught up iff its last
//     record abuts the tail, and behind otherwise. The signal flips as the
//     final record of such a batch is fetched.
//   - A batch without a tail (reading old data) marks the session as behind.
//   - An internal retry resets to behind until the new connection re-signals.
//
// Caught-up is session-relative and does not linearize: concurrent appends may
// already have advanced the true tail. For an authoritative tail, use
// CheckTail.
func (s *ReadSession) IsCaughtUp() bool {
	if s == nil || s.reader == nil {
		return false
	}
	return s.caughtUp.isCaughtUp()
}

// Returns a future that resolves when the session reaches the live tail.
// See IsCaughtUp for what caught up means.
func (s *ReadSession) CaughtUp() *CaughtUpFuture {
	if s == nil || s.reader == nil {
		return &CaughtUpFuture{}
	}
	return &CaughtUpFuture{state: &s.caughtUp}
}

// Represents the session catching up to the stream's tail.
// Call Wait to block until it does.
type CaughtUpFuture struct {
	state *caughtUpState
}

// Blocks until the session reaches the live tail, returning the last observed
// tail position at that moment. Returns immediately if already caught up.
// Wait again after falling behind to await the next catch-up; a pending Wait
// stays pending across internal retries.
//
// Returns ErrSessionEndedBeforeCaughtUp if the session terminates before
// reaching the tail, whether due to a fatal error or a limit (`count`,
// `bytes`, or `until`) being met.
//
// The signal advances only as records are consumed, so this must run
// concurrently with the Next loop, e.g. from a separate goroutine:
//
//	go func() {
//		tail, err := session.CaughtUp().Wait(ctx)
//		...
//	}()
//	for session.Next() {
//		...
//	}
func (f *CaughtUpFuture) Wait(ctx context.Context) (StreamPosition, error) {
	if f == nil || f.state == nil {
		return StreamPosition{}, ErrSessionEndedBeforeCaughtUp
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return f.state.wait(ctx)
}

// Returns the next read position, if known.
func (s *ReadSession) NextReadPosition() *StreamPosition {
	if s == nil || s.reader == nil {
		return nil
	}
	return s.reader.NextReadPosition()
}

// Returns the last observed tail position, if known.
func (s *ReadSession) LastObservedTail() *StreamPosition {
	if s == nil || s.reader == nil {
		return nil
	}
	return s.reader.LastObservedTail()
}
