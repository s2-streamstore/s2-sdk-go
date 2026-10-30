package s2

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func drainBatchRecords(batcher *Batcher) int {
	total := 0
	for {
		select {
		case batch, ok := <-batcher.Batches():
			if !ok {
				return total
			}
			total += len(batch.Input.Records)
		default:
			return total
		}
	}
}

// Add must surface the context error rather than nil once the batcher context
// is already canceled, otherwise records vanish with no signal (the documented
// batcher.Add(record, nil) migration pattern has no other error channel).
func TestBatcher_AddReturnsCtxErrWhenAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    10,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	defer batcher.Close()

	cancel()

	for i := range 5 {
		err := batcher.Add(AppendRecord{Body: []byte("x")}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("add %d after cancel: want context.Canceled, got %v", i, err)
		}
	}

	if got := drainBatchRecords(batcher); got != 0 {
		t.Fatalf("expected no emitted records after canceled adds, got %d", got)
	}
}

// addWithPermit must propagate the error from the pre-add flush (the one that
// flushes a preceding buffer that would overflow) when its send to the batches
// channel loses the select to ctx.Done(); previously the error was discarded
// and Add returned nil, leaving the new record apparently accepted.
//
// The single channel slot is pre-filled so the flush's send cannot succeed and
// must block until ctx.Done() wins the select. waitForLockedBatcher waits until
// the worker is blocked inside flushLocked (holding b.mu) before canceling,
// which guarantees the context was still alive at the top-of-add check and the
// surfaced error comes from the propagated flushLocked call rather than the
// early check.
func TestBatcher_AddPropagatesFlushErrorWhenCanceledDuringPreAddFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:      1000,
		MaxMeteredBytes: 10,
		Linger:          time.Hour,
		ChannelBuffer:   1,
	})
	defer batcher.Close()

	// A record of metered size 10 fills the channel via the post-add flush.
	seed := AppendRecord{Body: []byte("zz")}
	if MeteredPayloadBytes(seed) != 10 {
		t.Fatalf("seed metered bytes = %d, want 10", MeteredPayloadBytes(seed))
	}
	if err := batcher.Add(seed, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := len(batcher.batchesCh); got != 1 {
		t.Fatalf("expected channel pre-filled, got len %d", got)
	}

	// A size-9 record stays buffered (below the byte threshold, no flush yet).
	a := AppendRecord{Body: []byte("a")}
	if err := batcher.Add(a, nil); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if len(batcher.buffer) != 1 {
		t.Fatalf("expected buffer to hold a, got %d", len(batcher.buffer))
	}

	// Adding another size-9 record overflows the buffer (9+9 > 10), hitting the
	// pre-add flush, whose send blocks on the full channel until ctx.Done().
	type result struct{ err error }
	resCh := make(chan result, 1)
	go func() {
		resCh <- result{err: batcher.Add(AppendRecord{Body: []byte("b")}, nil)}
	}()

	waitForLockedBatcher(t, batcher)
	cancel()

	select {
	case res := <-resCh:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("add during pre-add flush: want context.Canceled, got %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("add blocked in pre-add flush did not return after cancel")
	}

	batcher.mu.Lock()
	buffered := len(batcher.buffer)
	batcher.mu.Unlock()
	if buffered != 0 {
		t.Fatalf("expected buffer cleared after failed pre-add flush, still holding %d records", buffered)
	}

	if got := drainBatchRecords(batcher); got != 1 {
		t.Fatalf("expected only the seed record emitted, got %d", got)
	}
}

// addWithPermit must propagate the error from the post-add flush (threshold
// reached after appending the new record) when its send to the batches channel
// loses the select to ctx.Done(); previously the error was discarded and Add
// returned nil while the just-added record was dropped.
//
// Same determinism strategy as the pre-add test: the single channel slot is
// pre-filled so the flush blocks until ctx.Done() wins.
func TestBatcher_AddPropagatesFlushErrorWhenCanceledDuringPostAddFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 1,
	})
	defer batcher.Close()

	if err := batcher.Add(AppendRecord{Body: []byte("seed")}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	type result struct{ err error }
	resCh := make(chan result, 1)
	go func() {
		resCh <- result{err: batcher.Add(AppendRecord{Body: []byte("x")}, nil)}
	}()

	waitForLockedBatcher(t, batcher)
	cancel()

	select {
	case res := <-resCh:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("add during flush: want context.Canceled, got %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("add blocked in flush did not return after cancel")
	}

	batcher.mu.Lock()
	stillBuffered := len(batcher.buffer)
	batcher.mu.Unlock()
	if stillBuffered != 0 {
		t.Fatalf("expected buffer cleared after failed flush, still holding %d records", stillBuffered)
	}

	if got := drainBatchRecords(batcher); got != 1 {
		t.Fatalf("expected only the seed record emitted, got %d", got)
	}
}

// waitForLockedBatcher spins until the batcher mutex is held by another
// goroutine, i.e. a worker has entered addWithPermit and is blocked inside
// flushLocked waiting for ctx.Done(). It fails the test if the lock never
// becomes contended before the deadline.
func waitForLockedBatcher(t *testing.T, batcher *Batcher) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !batcher.mu.TryLock() {
			return
		}
		batcher.mu.Unlock()
		runtime.Gosched()
	}
	t.Fatal("worker never entered flushLocked (mutex never contended)")
}
