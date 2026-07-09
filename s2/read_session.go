package s2

import (
	"context"
	"sync/atomic"
)

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
		case batch, ok := <-s.reader.Records():
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
			s.pending = batch
			if s.yieldPending() {
				return true
			}

		case err, ok := <-s.reader.Errors():
			if !ok {
				select {
				case batch, ok := <-s.reader.Records():
					if !ok {
						s.Close()
						return false
					}
					s.pending = batch
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
