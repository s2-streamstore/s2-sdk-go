package bentobox

import (
	"context"
	"errors"
	"sync"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

var (
	ErrInputClosed  = errors.New("input closed")
	ErrNoCacheEntry = errors.New("no cache entry")
)

type (
	Stream  = string
	AckFunc = func(ctx context.Context, err error) error
)

// SeqNumCache is used to persist read progress across restarts.
//
// Get must return a non-nil error when no entry exists for the given stream
// (a cache miss). Returning ErrNoCacheEntry is the idiomatic way to signal a
// miss, but any non-nil error is treated the same: the consumer falls back to
// the configured InputStartSeqNum default (so InputStartSeqNumLatest tails from
// the end). Errors that are not ErrNoCacheEntry are also logged as a warning,
// since they may indicate a transient backend failure rather than a clean miss
// (e.g. redis.Nil and sql.ErrNoRows naturally surface as errors and can be
// returned as-is).
//
// Do NOT return (0, nil) for a miss: the interface cannot distinguish that from
// a real cached position of 0, so it is interpreted as "resume from sequence
// number 0" and causes InputStartSeqNumLatest to replay from the beginning
// instead of tailing.
//
// Set must durably store the next sequence number to consume for the stream.
type SeqNumCache interface {
	Get(ctx context.Context, stream string) (uint64, error)
	Set(ctx context.Context, stream string, seqNum uint64) error
}

type seqNumCache struct {
	inner  SeqNumCache
	logger Logger
	// We don't trust the user to provide a valid cache so we have our own layer
	// on top of the one provided by the user.
	mem cmap.ConcurrentMap[string, uint64]
	// A 416 tail reset bumps a stream's generation. Acks carry the generation
	// captured when their session started and are dropped if it's now stale, so
	// an ack from before a reset can't clobber the corrected tail position. The
	// mutex makes a stream's generation check, mem write and durable write one
	// atomic step.
	gens cmap.ConcurrentMap[string, *streamGen]
}

type streamGen struct {
	mu  sync.Mutex
	gen uint64
}

func newSeqNumCache(inner SeqNumCache, logger Logger) *seqNumCache {
	return &seqNumCache{
		inner:  inner,
		logger: logger,
		mem:    cmap.New[uint64](),
		gens:   cmap.New[*streamGen](),
	}
}

func (s *seqNumCache) streamGenFor(stream string) *streamGen {
	return s.gens.Upsert(stream, nil, func(exists bool, cur, _ *streamGen) *streamGen {
		if exists {
			return cur
		}
		return &streamGen{}
	})
}

func (s *seqNumCache) generation(stream string) uint64 {
	guard := s.streamGenFor(stream)
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.gen
}

func (s *seqNumCache) Get(ctx context.Context, stream string) (uint64, error) {
	if val, ok := s.mem.Get(stream); ok {
		return val, nil
	}

	if s.inner == nil {
		return 0, ErrNoCacheEntry
	}

	cached, err := s.inner.Get(ctx, stream)
	if err != nil {
		// Any error from the inner cache is treated as no entry so the caller
		// falls back to the configured default start position rather than
		// blocking indefinitely. ErrNoCacheEntry is the idiomatic miss signal,
		// so only log when the error is something else: it may indicate a
		// transient backend failure rather than a clean miss.
		if !errors.Is(err, ErrNoCacheEntry) {
			s.logger.With("stream", stream, "error", err).Warn("Cache lookup failed, starting from default position")
		}
		return 0, ErrNoCacheEntry
	}

	s.mem.Set(stream, cached)

	return cached, nil
}

// Set advances the cached position from an ack. gen is dropped if it's older
// than the stream's current generation: the ack predates a 416 reset and would
// move the position back beyond the tail.
func (s *seqNumCache) Set(ctx context.Context, stream string, seqNum, gen uint64) error {
	guard := s.streamGenFor(stream)
	guard.mu.Lock()
	defer guard.mu.Unlock()

	if gen < guard.gen {
		s.logger.With("stream", stream, "seq_num", seqNum).Debug("Dropping ack from before a 416 tail reset")
		return nil
	}

	s.mem.Set(stream, seqNum)
	if s.inner != nil {
		if err := s.inner.Set(ctx, stream, seqNum); err != nil {
			return err
		}
	}
	return nil
}

// ResetToTail moves the cached position to the stream tail after a 416. Unlike
// Set it may move backward, and it bumps the generation so in-flight acks from
// before the reset are dropped. mem is written even if the durable write fails,
// so the reset survives in-process.
func (s *seqNumCache) ResetToTail(ctx context.Context, stream string, seqNum uint64) error {
	guard := s.streamGenFor(stream)
	guard.mu.Lock()
	defer guard.mu.Unlock()

	guard.gen++

	s.mem.Set(stream, seqNum)
	if s.inner != nil {
		if err := s.inner.Set(ctx, stream, seqNum); err != nil {
			return err
		}
	}
	return nil
}

// Drops the in-memory entry for stream so a subsequent Get re-reads the inner
// durable cache. Used when a stream leaves the configured set: if it returns
// later (possibly delete/recreated), we don't shadow the inner cache with a
// stale value held only in memory.
func (s *seqNumCache) Forget(stream string) {
	s.mem.Remove(stream)
}

type InputStreams interface {
	list(ctx context.Context, basin *s2.BasinClient) ([]string, error)
}

type StaticInputStreams struct {
	Streams []string
}

func (s StaticInputStreams) list(context.Context, *s2.BasinClient) ([]string, error) {
	return s.Streams, nil
}

type PrefixedInputStreams struct {
	Prefix string
}

func (p PrefixedInputStreams) list(ctx context.Context, basin *s2.BasinClient) ([]string, error) {
	var streams []string

	iter := basin.Streams.Iter(ctx, &s2.ListStreamsArgs{Prefix: p.Prefix})
	for iter.Next() {
		info := iter.Value()
		if info.DeletedAt == nil {
			streams = append(streams, string(info.Name))
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}

	return streams, nil
}

type InputStartSeqNum uint

const (
	InputStartSeqNumEarliest InputStartSeqNum = iota
	InputStartSeqNumLatest
)

type InputConfig struct {
	*Config
	Streams               InputStreams
	MaxInFlight           int
	UpdateStreamsInterval time.Duration
	Cache                 SeqNumCache
	Logger                Logger
	BackoffDuration       time.Duration
	StartSeqNum           InputStartSeqNum
}

type recvOutput struct {
	Stream  string
	Batch   []s2.SequencedRecord
	AckFunc AckFunc
	Err     error
}

type streamWorker struct {
	cancel context.CancelFunc
	closer <-chan struct{}
}

func (sw *streamWorker) Close() {
	sw.cancel()
}

func (sw *streamWorker) Wait() {
	<-sw.closer
}
