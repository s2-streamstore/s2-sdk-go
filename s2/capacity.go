package s2

import (
	"context"
	"fmt"
	"sync"
)

// used for inflight bytes/items to block appending till we have more cap
type capacityTracker struct {
	mu       sync.Mutex
	cond     *sync.Cond
	maxBytes int64
	maxItems int
	curBytes int64
	curItems int
	closed   bool
}

func newCapacityTracker(maxBytes int64, maxItems int) *capacityTracker {
	ct := &capacityTracker{
		maxBytes: maxBytes,
		maxItems: maxItems,
	}
	ct.cond = sync.NewCond(&ct.mu)
	return ct
}

// blocks until there is room for the requested number of bytes
func (c *capacityTracker) reserve(ctx context.Context, bytes int64) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if c.maxBytes > 0 && bytes > c.maxBytes {
		return fmt.Errorf("batch size %d exceeds max inflight bytes %d", bytes, c.maxBytes)
	}
	if c.maxItems != 0 && c.maxItems < 1 {
		return fmt.Errorf("max inflight batches must be at least 1, got %d", c.maxItems)
	}

	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		case <-waitDone:
		}
	}()
	defer close(waitDone)

	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.closed {
			return ErrSessionClosed
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if c.hasCapacity(bytes) {
			c.curBytes += bytes
			if c.maxItems > 0 {
				c.curItems++
			}
			return nil
		}
		c.cond.Wait()
	}
}

func (c *capacityTracker) release(bytes int64) {
	c.mu.Lock()
	if bytes > 0 {
		c.curBytes -= bytes
		if c.curBytes < 0 {
			c.curBytes = 0
		}
	}
	if c.maxItems > 0 && c.curItems > 0 {
		c.curItems--
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

// wakes all waiters and causes future Reserve calls to fail.
func (c *capacityTracker) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *capacityTracker) hasCapacity(bytes int64) bool {
	if c.maxBytes > 0 && c.curBytes+bytes > c.maxBytes {
		return false
	}
	if c.maxItems > 0 && c.curItems >= c.maxItems {
		return false
	}
	return true
}
