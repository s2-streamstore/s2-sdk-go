package bentobox

import (
	"context"
	"errors"
	"sync"
	"testing"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

func newTestStreamInput() *streamInput {
	cache := newSeqNumCache(nil, silentLogger{})
	return &streamInput{
		Stream:   "demo",
		cache:    cache,
		toAck:    newToAckMap("demo", cache, silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}
}

// Calling the returned AckFunc more than once with an error must not enqueue
// the same batch for redelivery more than once.
func TestAckFuncDoubleNackEnqueuesOnce(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	boom := errors.New("boom")
	_ = ackFunc(context.Background(), boom)
	_ = ackFunc(context.Background(), boom)

	if got := len(si.nacks); got != 1 {
		t.Fatalf("expected batch enqueued for redelivery exactly once, got %d", got)
	}
}

// Once a batch is acked, a subsequent invocation with an error must not cause a
// redelivery (which would reprocess already-committed records).
func TestAckFuncAckThenNackDoesNotRedeliver(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	if err := ackFunc(context.Background(), nil); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	_ = ackFunc(context.Background(), errors.New("boom"))

	if got := len(si.nacks); got != 0 {
		t.Fatalf("expected no redelivery after ack, got %d", got)
	}
}

// Subsequent invocations must return the same result as the first.
func TestAckFuncReturnsCachedResult(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	first := ackFunc(context.Background(), nil)
	second := ackFunc(context.Background(), nil)

	if first != nil {
		t.Fatalf("first ack returned error: %v", first)
	}
	if second != first {
		t.Fatalf("expected cached result %v on repeat, got %v", first, second)
	}
}

// fakeCache is a durable SeqNumCache that records the last persisted value.
type fakeCache struct {
	mu     sync.Mutex
	values map[string]uint64
}

func newFakeCache() *fakeCache { return &fakeCache{values: map[string]uint64{}} }

func (f *fakeCache) Get(_ context.Context, stream string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[stream], nil
}

func (f *fakeCache) Set(_ context.Context, stream string, seqNum uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[stream] = seqNum
	return nil
}

// Reproduces issue #294: an ack that completes after the streamInput is closed
// returns success but does not persist the sequence position, so a re-add of
// the stream resumes from the stale position and re-delivers acked records.
func TestAckAfterCloseDoesNotUpdateCache(t *testing.T) {
	const stream = "demo"
	inner := newFakeCache()
	cache := newSeqNumCache(inner, silentLogger{})
	si := &streamInput{
		Stream:   stream,
		cache:    cache,
		toAck:    newToAckMap(stream, cache, silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}

	records := []s2.SequencedRecord{{SeqNum: 100}, {SeqNum: 101}, {SeqNum: 102}}
	si.toAck.Add(records)

	// Simulate the streamInput having been closed (session restart / removal /
	// shutdown) while this batch's ack is still pending in the Bento pipeline.
	close(si.closedCh)

	if err := si.ack(context.Background(), records); err != nil {
		t.Fatalf("ack returned error: %v", err)
	}

	got, _ := inner.Get(context.Background(), stream)
	if got != 103 {
		t.Fatalf("expected durable cache to advance to 103 after ack, got %d", got)
	}
}
