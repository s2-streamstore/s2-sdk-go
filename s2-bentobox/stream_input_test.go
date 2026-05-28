package bentobox

import (
	"context"
	"errors"
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
