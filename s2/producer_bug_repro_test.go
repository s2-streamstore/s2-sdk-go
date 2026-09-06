package s2

import (
	"context"
	"errors"
	"testing"
)

// TestProducer_ReproBarrierTerminalError reproduces the bug where an in-flight
// Flush returns a spurious context.Canceled (sourced from the producer's
// global sticky terminal error set by AfterFunc(prodCtx, ...)) even though
// every record covered by the flush was durably acknowledged.
//
// The race window requires:
//   - A parent-context cancellation that fires AfterFunc(prodCtx, ...) while a
//     Flush is already past its pre-check.
//   - The covered append's ack resolving with success (the server acknowledged
//     just before the cancel propagated to the session) so ticket.Ack() returns
//     a durable IndexedAppendAck.
//
// The barrier in consumeBatches must report only terminal errors sourced from
// covered append outcomes (or nil if every covered ack succeeded), rather than
// the producer's global sticky terminalErr, so that Flush's return value stays
// consistent with ticket.Ack().
//
// This runs many iterations because the race that lets the covered ack drain
// with success is timing-sensitive; across the run the window reliably opens.
func TestProducer_ReproBarrierTerminalError(t *testing.T) {
	const iterations = 50

	durableCount := 0
	for i := range iterations {
		// Batcher on an independent context so canceling the producer's parent
		// context only fires the prodCtx AfterFunc, not the batcher shutdown
		// callback. MaxRecords > submitted records ensures the batch is flushed
		// by Flush's flushBarrier (holding the batcher mutex) rather than by
		// Submit, so the barrier is guaranteed to be established before the
		// cancel races it: the batcher close triggered by recordTerminalError
		// cannot acquire the mutex until flushBarrier has sent the barrier.
		batcher := NewBatcher(context.Background(), &BatchingOptions{
			MaxRecords:    2,
			Linger:        flushTestTimeout,
			ChannelBuffer: 10,
		})
		session := newControlledAppendSession()

		parentCtx, cancelParent := context.WithCancel(context.Background())
		producer := newProducerWithSession(parentCtx, batcher, session)

		ticket := submitRecord(t, producer, "record")

		// Independent flush context so the flush itself is not canceled by the
		// parent-context cancellation.
		flushDone := startFlush(producer, context.Background())

		call := nextAppendCall(t, session)
		call.accept() // ack goroutine parks in waitForBatchAck on ticket.ackCh

		// Cancel the parent context: AfterFunc(prodCtx) fires and eagerly
		// records the sticky terminal error (context.Canceled) regardless of
		// in-flight ack outcomes.
		cancelParent()

		// Immediately resolve the append ack with success so ticket.ackCh has
		// a buffered durable outcome. The ack goroutine drains it (either
		// directly or via the ctx.Done inner select) and resolveBatchAck marks
		// the record durable.
		call.resolve(appendAck(0, 1), nil)

		flushErr := waitFlush(t, flushDone)
		outcome := readyOutcome(t, ticket)

		// The barrier's return value must be consistent with the covered
		// ticket's outcome. When the covered append was durably acknowledged,
		// Flush must not report a terminal error.
		if outcome.err == nil {
			durableCount++
			if flushErr != nil {
				t.Fatalf("iter %d: covered ticket durable (seq=%d) but flush returned %v",
					i, outcome.indexedAck.SeqNum(), flushErr)
			}
		} else {
			// If the ack goroutine fell back to the ctx.Done default branch
			// (no buffered ack drained), the ticket fails with the sticky
			// terminal error and Flush must report the same error.
			if !errors.Is(flushErr, outcome.err) {
				t.Fatalf("iter %d: ticket failed (%v) but flush returned %v",
					i, outcome.err, flushErr)
			}
		}

		// By-design teardown: after the parent context is canceled, subsequent
		// Submit/Flush/Close return the cancellation error.
		if _, err := producer.Submit(AppendRecord{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("iter %d: submit after cancel: %v", i, err)
		}
		if err := producer.Flush(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("iter %d: flush after cancel: %v", i, err)
		}
		if err := producer.Close(); !errors.Is(err, context.Canceled) {
			t.Fatalf("iter %d: close after cancel: %v", i, err)
		}
	}

	// The bug window (covered ack draining with success while the parent
	// context is canceled) must open at least once for the test to exercise
	// the regression path. In practice it opens on the large majority of
	// iterations.
	if durableCount == 0 {
		t.Fatalf("covered ack never drained with success across %d iterations", iterations)
	}
}
