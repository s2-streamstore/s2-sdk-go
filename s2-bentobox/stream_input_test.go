package bentobox

import (
	"context"
	"errors"
	"testing"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type testLogger struct{}

func (l *testLogger) Tracef(string, ...any) {}
func (l *testLogger) Trace(string)          {}
func (l *testLogger) Debugf(string, ...any) {}
func (l *testLogger) Debug(string)          {}
func (l *testLogger) Infof(string, ...any)  {}
func (l *testLogger) Info(string)           {}
func (l *testLogger) Warnf(string, ...any)  {}
func (l *testLogger) Warn(string)           {}
func (l *testLogger) Errorf(string, ...any) {}
func (l *testLogger) Error(string)          {}
func (l *testLogger) With(...any) Logger    { return l }

type controllableCache struct {
	data    map[string]uint64
	failSet bool
}

func newControllableCache() *controllableCache {
	return &controllableCache{data: make(map[string]uint64)}
}

func (c *controllableCache) Get(_ context.Context, stream string) (uint64, error) {
	v, ok := c.data[stream]
	if !ok {
		return 0, ErrNoCacheEntry
	}
	return v, nil
}

func (c *controllableCache) Set(_ context.Context, stream string, seqNum uint64) error {
	if c.failSet {
		return errors.New("simulated cache failure")
	}
	c.data[stream] = seqNum
	return nil
}

// Issue #265: a transient cache.Set failure must not silently discard the
// in-memory tracking of the batch. If the user (or framework) retries the
// batch's ack after the transient failure clears, the SDK must be able to
// persist the missing progress; otherwise the cache can be advanced past the
// failed range by a later batch's success, with no recovery path.
func TestToAckMap_MarkDone_RecoversFromTransientCacheFailure(t *testing.T) {
	ctx := context.Background()
	stream := "test-stream"

	mock := newControllableCache()
	cache := newSeqNumCache(mock, &testLogger{})
	tam := newToAckMap(stream, cache, &testLogger{})

	batch0 := []s2.SequencedRecord{{SeqNum: 0}, {SeqNum: 1}, {SeqNum: 2}}
	batch1 := []s2.SequencedRecord{{SeqNum: 3}, {SeqNum: 4}, {SeqNum: 5}}
	batch2 := []s2.SequencedRecord{{SeqNum: 6}, {SeqNum: 7}, {SeqNum: 8}}

	tam.Add(batch0)
	tam.Add(batch1)
	tam.Add(batch2)

	if err := tam.MarkDone(ctx, batch0, true); err != nil {
		t.Fatalf("batch0 ack: %v", err)
	}
	if got := mock.data[stream]; got != 3 {
		t.Fatalf("after batch0: cache = %d, want 3", got)
	}

	mock.failSet = true
	if err := tam.MarkDone(ctx, batch1, true); err == nil {
		t.Fatal("batch1 ack: expected error from cache.Set, got nil")
	}
	if got := mock.data[stream]; got != 3 {
		t.Fatalf("after failed batch1: cache = %d, want 3 (unchanged)", got)
	}
	mock.failSet = false

	// The transient failure has cleared. Retrying batch1's ack must succeed and
	// bring the cache up to 6 — proving batch1 is still tracked and recoverable.
	// In the buggy implementation, batch1 has been deleted from the in-memory
	// map at this point, so this retry returns errCannotAckBatch.
	if err := tam.MarkDone(ctx, batch1, true); err != nil {
		t.Fatalf("batch1 retry after transient failure: got error %v; batch must remain trackable", err)
	}
	if got := mock.data[stream]; got != 6 {
		t.Fatalf("after batch1 retry: cache = %d, want 6", got)
	}

	if err := tam.MarkDone(ctx, batch2, true); err != nil {
		t.Fatalf("batch2 ack: %v", err)
	}
	if got := mock.data[stream]; got != 9 {
		t.Fatalf("after batch2: cache = %d, want 9", got)
	}
}
