package s2

import (
	"context"
	"sync"
	"sync/atomic"
)

// ErrSessionEndedBeforeCaughtUp is an alias for ErrSessionClosed.
// Deprecated: use ErrSessionClosed.
var ErrSessionEndedBeforeCaughtUp = ErrSessionClosed

type caughtUpResult struct {
	done chan struct{}
	tail StreamPosition
	err  error
}

// caughtUpState stores the latest server report and completes one-shot waits.
type caughtUpState struct {
	mu      sync.Mutex
	tail    *StreamPosition
	ended   bool
	endErr  error
	pending []*caughtUpResult
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
	for _, result := range c.pending {
		result.tail = tail
		close(result.done)
	}
	c.pending = nil
}

func (c *caughtUpState) setBehind() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.tail = nil
}

// end marks the session done. A normal end preserves an already-observed tail.
func (c *caughtUpState) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.ended = true
	if err == nil {
		err = ErrSessionClosed
	} else {
		c.tail = nil
	}
	c.endErr = err
	for _, result := range c.pending {
		result.err = err
		close(result.done)
	}
	c.pending = nil
}

func (c *caughtUpState) newResult() *caughtUpResult {
	result := &caughtUpResult{done: make(chan struct{})}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail != nil {
		result.tail = *c.tail
		close(result.done)
		return result
	}
	if c.ended {
		result.err = c.endErr
		close(result.done)
		return result
	}
	c.pending = append(c.pending, result)
	return result
}

type ReadSession struct {
	reader  *streamReader
	pending []SequencedRecord
	current SequencedRecord
	err     error
	closed  atomic.Bool
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
		case records, ok := <-s.reader.Records():
			if !ok {
				// Records closed - drain any pending error before exiting
				select {
				case err := <-s.reader.Errors():
					if err != nil {
						s.err = err
					}
				default:
				}
				s.Close()
				return false
			}
			s.pending = records
			if s.yieldPending() {
				return true
			}

		case err, ok := <-s.reader.Errors():
			if !ok {
				select {
				case records, ok := <-s.reader.Records():
					if !ok {
						s.Close()
						return false
					}
					s.pending = records
					if s.yieldPending() {
						return true
					}
				default:
					s.Close()
					return false
				}
			}
			if err != nil {
				s.err = err
				s.Close()
				return false
			}
		}
	}
}

func (s *ReadSession) yieldPending() bool {
	if len(s.pending) == 0 {
		return false
	}
	s.current = s.pending[0]
	s.pending[0] = SequencedRecord{} // release references so consumed records can be collected
	s.pending = s.pending[1:]
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
	return s.reader.Close()
}

// IsCaughtUp reports whether the session has reached the tail in the latest
// server update. Ignored command records still advance the read position.
//
// A heartbeat reports caught up. A record batch reports caught up only when
// its last record ends at the reported tail. A batch without a tail, or a
// reconnect, reports behind. New records may arrive after any report; use
// CheckTail to fetch the stream's current tail.
func (s *ReadSession) IsCaughtUp() bool {
	if s == nil || s.reader == nil || s.reader.caughtUp == nil {
		return false
	}
	return s.reader.caughtUp.isCaughtUp()
}

// CaughtUp returns a one-shot future for the next caught-up state. If the
// session is already caught up, the future contains the currently reported
// tail. A pending future stays pending across reconnects. Call CaughtUp again
// after the session falls behind.
func (s *ReadSession) CaughtUp() *CaughtUpFuture {
	if s == nil || s.reader == nil || s.reader.caughtUp == nil {
		return &CaughtUpFuture{}
	}
	return &CaughtUpFuture{result: s.reader.caughtUp.newResult()}
}

// CaughtUpFuture lets callers wait for a read session to reach a reported tail.
type CaughtUpFuture struct {
	result *caughtUpResult
}

// Wait blocks until the session reaches a tail reported by the server and
// returns that tail. If the session ends first, Wait returns the read error or
// ErrSessionClosed. A canceled context only stops that call to Wait; the future
// keeps its one-shot result for a later call.
func (f *CaughtUpFuture) Wait(ctx context.Context) (StreamPosition, error) {
	if f == nil || f.result == nil {
		return StreamPosition{}, ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-f.result.done:
		return f.result.tail, f.result.err
	default:
	}
	select {
	case <-f.result.done:
		return f.result.tail, f.result.err
	case <-ctx.Done():
		return StreamPosition{}, ctx.Err()
	}
}

// Returns the next read position, if known. Ignored command records are
// included in this position.
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
