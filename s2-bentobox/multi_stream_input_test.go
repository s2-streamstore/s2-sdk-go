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
	if err := cache.Set(context.Background(), stream, 42, 0); err != nil {
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

// Regression for #328: an ack from a session that started before a 416 tail
// reset lands asynchronously after the reset. It must not overwrite the
// corrected tail position in either the in-memory or durable cache, otherwise
// the next reconnect reads a beyond-tail position and hits 416 again.
func TestStaleAckDoesNotOverwrite416TailReset(t *testing.T) {
	const stream = "demo"
	const tailSeqNum uint64 = 100
	const staleSeqNum uint64 = 500

	inner := &mapCache{data: map[string]uint64{stream: staleSeqNum}}
	cache := newSeqNumCache(inner, silentLogger{})

	// The old session captured the pre-reset generation.
	oldSi := &streamInput{
		Stream:   stream,
		cache:    cache,
		toAck:    newToAckMap(stream, cache, cache.generation(stream), silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}

	records := []s2.SequencedRecord{{SeqNum: staleSeqNum - 2}, {SeqNum: staleSeqNum - 1}, {SeqNum: staleSeqNum}}
	_, ackFunc, err := oldSi.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	rangeErr := &s2.RangeNotSatisfiableError{
		S2Error: &s2.S2Error{Status: 416, Code: "RANGE_NOT_SATISFIABLE", Origin: "server"},
		Tail:    &s2.StreamPosition{SeqNum: tailSeqNum},
	}
	src := &fakeBatchSource{results: []readResult{{err: rangeErr}}}
	streamSourceRecvLoop(context.Background(), src, cache, stream, make(chan recvOutput, 1), silentLogger{})

	if got, ok := cache.mem.Get(stream); !ok || got != tailSeqNum {
		t.Fatalf("after 416, expected mem at tail %d, got %d (present=%v)", tailSeqNum, got, ok)
	}

	// The stale ack now completes; it predates the reset and must be dropped.
	if err := ackFunc(context.Background(), nil); err != nil {
		t.Fatalf("stale ack returned error: %v", err)
	}

	if got, ok := cache.mem.Get(stream); !ok || got != tailSeqNum {
		t.Errorf("stale ack corrupted mem: expected tail %d, got %d", tailSeqNum, got)
	}
	if got, err := inner.Get(context.Background(), stream); err != nil || got != tailSeqNum {
		t.Errorf("stale ack corrupted durable cache: expected tail %d, got %d (err=%v)", tailSeqNum, got, err)
	}
}

// A session started after a 416 reset captures the bumped generation, so its
// acks advance the cache normally rather than being dropped as stale.
func TestAckAfterTailResetAdvancesCache(t *testing.T) {
	const stream = "demo"
	const tailSeqNum uint64 = 100

	inner := &mapCache{data: map[string]uint64{}}
	cache := newSeqNumCache(inner, silentLogger{})

	if err := cache.ResetToTail(context.Background(), stream, tailSeqNum); err != nil {
		t.Fatalf("reset to tail: %v", err)
	}

	newSi := &streamInput{
		Stream:   stream,
		cache:    cache,
		toAck:    newToAckMap(stream, cache, cache.generation(stream), silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}

	records := []s2.SequencedRecord{{SeqNum: tailSeqNum}, {SeqNum: tailSeqNum + 1}}
	if _, ackFunc, err := newSi.handleBatch(context.Background(), records); err != nil {
		t.Fatalf("handleBatch: %v", err)
	} else if err := ackFunc(context.Background(), nil); err != nil {
		t.Fatalf("ack: %v", err)
	}

	want := tailSeqNum + 2
	if got, ok := cache.mem.Get(stream); !ok || got != want {
		t.Errorf("expected mem advanced to %d, got %d", want, got)
	}
	if got, err := inner.Get(context.Background(), stream); err != nil || got != want {
		t.Errorf("expected durable cache advanced to %d, got %d (err=%v)", want, got, err)
	}
}

func TestStreamSourceRecvLoopForwardsOtherErrors(t *testing.T) {
	const stream = "demo"
	otherErr := errors.New("boom")

	src := &fakeBatchSource{
		results: []readResult{{err: otherErr}},
	}
	cache := newSeqNumCache(nil, silentLogger{})
	if err := cache.Set(context.Background(), stream, 42, 0); err != nil {
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
	if err := cache.Set(context.Background(), stream, 42, 0); err != nil {
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
	if err := cache.Set(context.Background(), "s", 7, 0); err != nil {
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

func TestStreamSourceRecvLoopKeepsTailInMemWhenSetFails(t *testing.T) {
	const stream = "demo"
	const stale uint64 = 42
	const tailSeqNum uint64 = 9001

	// A durable cache stuck at a stale (beyond-tail) value whose writes fail.
	inner := &failingSetCache{getValue: stale, setErr: errors.New("durable cache write failed")}
	cache := newSeqNumCache(inner, silentLogger{})

	// Warm the in-memory layer with the stale value.
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

	// The durable write failed, but the in-memory position must still be reset
	// to the tail so the next reconnect doesn't fall back to the stale value.
	if got, ok := cache.mem.Get(stream); !ok || got != tailSeqNum {
		t.Fatalf("expected in-memory position reset to tail %d, got %d (present=%v)", tailSeqNum, got, ok)
	}

	got, err := cache.Get(context.Background(), stream)
	if err != nil {
		t.Fatalf("get after 416 reset: %v", err)
	}
	if got != tailSeqNum {
		t.Fatalf("next reconnect should read from tail %d, got stale %d", tailSeqNum, got)
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
