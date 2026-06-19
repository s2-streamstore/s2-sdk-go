package bentobox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type fakeBatchSource struct {
	mu       sync.Mutex
	results  []readResult
	pos      int
	closeErr error
	closed   bool
}

type readResult struct {
	batch   []s2.SequencedRecord
	ackFunc AckFunc
	err     error
}

func (f *fakeBatchSource) ReadBatch(ctx context.Context) ([]s2.SequencedRecord, AckFunc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pos >= len(f.results) {
		return nil, nil, ErrInputClosed
	}
	r := f.results[f.pos]
	f.pos++
	return r.batch, r.ackFunc, r.err
}

func (f *fakeBatchSource) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

type silentLogger struct{}

func (silentLogger) Tracef(string, ...any)            {}
func (silentLogger) Trace(string)                     {}
func (silentLogger) Debugf(string, ...any)            {}
func (silentLogger) Debug(string)                     {}
func (silentLogger) Infof(string, ...any)             {}
func (silentLogger) Info(string)                      {}
func (silentLogger) Warnf(string, ...any)             {}
func (silentLogger) Warn(string)                      {}
func (silentLogger) Errorf(string, ...any)            {}
func (silentLogger) Error(string)                     {}
func (silentLogger) With(...any) Logger               { return silentLogger{} }

func TestStreamSourceRecvLoopResetsCacheOn416(t *testing.T) {
	const stream = "demo"
	const tailSeqNum uint64 = 9001

	rangeErr := &s2.RangeNotSatisfiableError{
		S2Error: &s2.S2Error{
			Status: 416,
			Code:   "RANGE_NOT_SATISFIABLE",
			Origin: "server",
		},
		Tail: &s2.StreamPosition{SeqNum: tailSeqNum},
	}

	src := &fakeBatchSource{
		results: []readResult{{err: rangeErr}},
	}
	cache := newSeqNumCache(nil, silentLogger{})
	if err := cache.Set(context.Background(), stream, 42); err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	inputStream := make(chan recvOutput, 1)
	done := make(chan struct{})
	go func() {
		streamSourceRecvLoop(context.Background(), src, cache, stream, inputStream, silentLogger{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recv loop did not return on 416")
	}

	got, err := cache.Get(context.Background(), stream)
	if err != nil {
		t.Fatalf("cache.Get after 416: %v", err)
	}
	if got != tailSeqNum {
		t.Fatalf("expected cache reset to tail %d, got %d", tailSeqNum, got)
	}

	select {
	case out := <-inputStream:
		t.Fatalf("416 should be swallowed; got recvOutput %+v", out)
	default:
	}

	if !src.closed {
		t.Fatal("recv loop should close the input")
	}
}

func TestStreamSourceRecvLoopForwardsOtherErrors(t *testing.T) {
	const stream = "demo"
	otherErr := errors.New("boom")

	src := &fakeBatchSource{
		results: []readResult{{err: otherErr}},
	}
	cache := newSeqNumCache(nil, silentLogger{})
	if err := cache.Set(context.Background(), stream, 42); err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	inputStream := make(chan recvOutput, 1)
	done := make(chan struct{})
	go func() {
		streamSourceRecvLoop(context.Background(), src, cache, stream, inputStream, silentLogger{})
		close(done)
	}()

	select {
	case out := <-inputStream:
		if !errors.Is(out.Err, otherErr) {
			t.Fatalf("expected forwarded err, got %v", out.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected error to be forwarded to inputStream")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recv loop did not return after forwarding error")
	}

	got, err := cache.Get(context.Background(), stream)
	if err != nil {
		t.Fatalf("cache.Get: %v", err)
	}
	if got != 42 {
		t.Fatalf("non-416 errors should not touch cache; got %d", got)
	}
}

func TestStreamSourceRecvLoopForwards416WhenTailMissing(t *testing.T) {
	const stream = "demo"

	rangeErr := &s2.RangeNotSatisfiableError{
		S2Error: &s2.S2Error{Status: 416, Code: "RANGE_NOT_SATISFIABLE", Origin: "server"},
	}

	src := &fakeBatchSource{
		results: []readResult{{err: rangeErr}},
	}
	cache := newSeqNumCache(nil, silentLogger{})
	if err := cache.Set(context.Background(), stream, 42); err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	inputStream := make(chan recvOutput, 1)
	done := make(chan struct{})
	go func() {
		streamSourceRecvLoop(context.Background(), src, cache, stream, inputStream, silentLogger{})
		close(done)
	}()

	select {
	case out := <-inputStream:
		var got *s2.RangeNotSatisfiableError
		if !errors.As(out.Err, &got) {
			t.Fatalf("expected RangeNotSatisfiableError forwarded, got %T", out.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected 416 without tail to be forwarded")
	}

	<-done

	got, err := cache.Get(context.Background(), stream)
	if err != nil {
		t.Fatalf("cache.Get: %v", err)
	}
	if got != 42 {
		t.Fatalf("cache should be untouched without Tail; got %d", got)
	}
}

func TestSeqNumCacheForgetDropsInMemoryEntry(t *testing.T) {
	cache := newSeqNumCache(nil, silentLogger{})
	if err := cache.Set(context.Background(), "s", 7); err != nil {
		t.Fatalf("set: %v", err)
	}

	cache.Forget("s")

	_, err := cache.Get(context.Background(), "s")
	if !errors.Is(err, ErrNoCacheEntry) {
		t.Fatalf("expected ErrNoCacheEntry after Forget, got %v", err)
	}
}

func TestSeqNumCacheForgetDoesNotTouchInner(t *testing.T) {
	inner := &mapCache{data: map[string]uint64{"s": 11}}
	cache := newSeqNumCache(inner, silentLogger{})

	if _, err := cache.Get(context.Background(), "s"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	cache.Forget("s")

	got, err := cache.Get(context.Background(), "s")
	if err != nil {
		t.Fatalf("get after forget: %v", err)
	}
	if got != 11 {
		t.Fatalf("expected inner durable value 11, got %d", got)
	}
}

type mapCache struct {
	mu   sync.Mutex
	data map[string]uint64
}

func (m *mapCache) Get(_ context.Context, stream string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[stream]
	if !ok {
		return 0, ErrNoCacheEntry
	}
	return v, nil
}

func (m *mapCache) Set(_ context.Context, stream string, seqNum uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[stream] = seqNum
	return nil
}

type failingSetCache struct {
	getValue uint64
	setErr   error
}

func (f *failingSetCache) Get(context.Context, string) (uint64, error) {
	return f.getValue, nil
}

func (f *failingSetCache) Set(context.Context, string, uint64) error {
	return f.setErr
}

func TestStreamSourceRecvLoopForgetsStaleMemEntryWhenSetFails(t *testing.T) {
	const stream = "demo"
	const stale uint64 = 42
	const tailSeqNum uint64 = 9001

	inner := &failingSetCache{getValue: stale, setErr: errors.New("durable cache write failed")}
	cache := newSeqNumCache(inner, silentLogger{})

	// Warm the in-memory layer with the stale value (simulates prior successful
	// Set that has since become stale relative to the stream tail).
	if _, err := cache.Get(context.Background(), stream); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	rangeErr := &s2.RangeNotSatisfiableError{
		S2Error: &s2.S2Error{Status: 416, Code: "RANGE_NOT_SATISFIABLE", Origin: "server"},
		Tail:    &s2.StreamPosition{SeqNum: tailSeqNum},
	}
	src := &fakeBatchSource{results: []readResult{{err: rangeErr}}}
	inputStream := make(chan recvOutput, 1)

	streamSourceRecvLoop(context.Background(), src, cache, stream, inputStream, silentLogger{})

	// In-mem must be cleared so the next reconnect falls through to inner.Get
	// instead of returning the stale value and tight-looping on 416.
	if _, ok := cache.mem.Get(stream); ok {
		t.Fatal("expected in-memory cache entry to be cleared after failed Set")
	}
}

type erroringCache struct {
	err error
}

func (c *erroringCache) Get(context.Context, string) (uint64, error) { return 0, c.err }
func (c *erroringCache) Set(context.Context, string, uint64) error   { return nil }

type warnCountingLogger struct {
	silentLogger
	warns *int
}

func (l warnCountingLogger) With(...any) Logger { return l }
func (l warnCountingLogger) Warn(string)        { *l.warns++ }
func (l warnCountingLogger) Warnf(string, ...any) {
	*l.warns++
}

// A clean miss signalled via ErrNoCacheEntry must surface as ErrNoCacheEntry
// without being logged as a failure; any other error is also surfaced as
// ErrNoCacheEntry but logged as a warning since it may be a transient backend
// fault rather than a real miss.
func TestSeqNumCacheGetMissLogging(t *testing.T) {
	t.Run("ErrNoCacheEntry does not warn", func(t *testing.T) {
		var warns int
		cache := newSeqNumCache(&erroringCache{err: ErrNoCacheEntry}, warnCountingLogger{warns: &warns})

		if _, err := cache.Get(context.Background(), "s"); !errors.Is(err, ErrNoCacheEntry) {
			t.Fatalf("expected ErrNoCacheEntry, got %v", err)
		}
		if warns != 0 {
			t.Fatalf("clean miss should not warn; got %d warnings", warns)
		}
	})

	t.Run("other error warns", func(t *testing.T) {
		var warns int
		cache := newSeqNumCache(&erroringCache{err: errors.New("backend down")}, warnCountingLogger{warns: &warns})

		if _, err := cache.Get(context.Background(), "s"); !errors.Is(err, ErrNoCacheEntry) {
			t.Fatalf("expected ErrNoCacheEntry, got %v", err)
		}
		if warns != 1 {
			t.Fatalf("backend error should warn once; got %d warnings", warns)
		}
	})
}
