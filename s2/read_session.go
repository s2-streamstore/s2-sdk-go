package s2

import (
	"context"
	"sync"
	"sync/atomic"
)

type caughtUpResult struct {
	done chan struct{}
	tail StreamPosition
	err  error
}

// caughtUpState stores the current tail state and pending futures.
type caughtUpState struct {
	mu          sync.Mutex
	tail        *StreamPosition
	terminalErr error
	pending     []*caughtUpResult
}

func (c *caughtUpState) isCaughtUp() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tail != nil
}

func (c *caughtUpState) setCaughtUp(tail StreamPosition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminalErr != nil {
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
	if c.terminalErr != nil {
		return
	}
	c.tail = nil
}

// end settles pending futures. A clean end keeps an observed tail.
func (c *caughtUpState) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminalErr != nil {
		return
	}
	if err == nil {
		err = ErrSessionClosed
	} else {
		c.tail = nil
	}
	c.terminalErr = err
	for _, result := range c.pending {
		result.err = err
		close(result.done)
	}
	c.pending = nil
}

func (c *caughtUpState) newFuture() *CaughtUpFuture {
	result := &caughtUpResult{done: make(chan struct{})}
	future := &CaughtUpFuture{result: result}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail != nil {
		result.tail = *c.tail
		close(result.done)
		return future
	}
	if c.terminalErr != nil {
		result.err = c.terminalErr
		close(result.done)
		return future
	}
	c.pending = append(c.pending, result)
	return future
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
		case records, ok := <-s.reader.recordsCh:
			if !ok {
				// Records closed - drain any pending error before exiting
				select {
				case err := <-s.reader.errorCh:
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

		case err, ok := <-s.reader.errorCh:
			if !ok {
				select {
				case records, ok := <-s.reader.recordsCh:
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

// IsCaughtUp reports whether the session has fetched through its latest reported tail.
// It becomes false after a gap, a batch without a tail, or a reconnect.
// Ignored command records count toward progress.
// Use [StreamClient.CheckTail] for the current stream tail.
func (s *ReadSession) IsCaughtUp() bool {
	if s == nil || s.reader == nil {
		return false
	}
	return s.reader.caughtUp.isCaughtUp()
}

// CaughtUp returns a future for the next caught-up state.
// It is ready immediately if the session is already caught up.
// It remains pending across reconnects.
// Call CaughtUp again after the session falls behind.
func (s *ReadSession) CaughtUp() *CaughtUpFuture {
	if s == nil || s.reader == nil {
		return &CaughtUpFuture{}
	}
	return s.reader.caughtUp.newFuture()
}

// CaughtUpFuture represents one caught-up state.
type CaughtUpFuture struct {
	result *caughtUpResult
}

// Wait returns the tail captured by the future.
// It returns the read error or ErrSessionClosed if the session ends first.
// Canceling ctx stops this call. The future can be waited on again.
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
