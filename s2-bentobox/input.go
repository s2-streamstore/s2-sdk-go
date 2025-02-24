package s2bentobox

import (
	"context"
	"errors"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/s2-streamstore/s2-sdk-go/s2"
)

var ErrInputClosed = errors.New("input closed")

type SeqNumCache interface {
	Get(ctx context.Context, stream string) (uint64, error)
	Set(ctx context.Context, stream string, seqNum uint64) error
}

type seqNumCache struct {
	inner SeqNumCache
	// We don't trust the user to provide a valid cache so we have our own layer
	// on top of the one provided by the user.
	mem cmap.ConcurrentMap[string, uint64]
}

func newSeqNumCache(inner SeqNumCache) *seqNumCache {
	return &seqNumCache{
		inner: inner,
		mem:   cmap.New[uint64](),
	}
}

func (s *seqNumCache) Get(ctx context.Context, stream string) (uint64, error) {
	seqNum, ok := s.mem.Get(stream)
	if ok {
		return seqNum, nil
	}

	cached, err := s.inner.Get(ctx, stream)
	if err != nil {
		return 0, err
	}

	s.mem.Set(stream, cached)

	return cached, nil
}

func (s *seqNumCache) Set(ctx context.Context, stream string, seqNum uint64) error {
	if err := s.inner.Set(ctx, stream, seqNum); err != nil {
		return err
	}

	s.mem.Set(stream, seqNum)

	return nil
}

type InputStreams interface {
	list(ctx context.Context, client *s2.BasinClient) ([]string, error)
}

type StaticInputStreams struct {
	Streams []string
}

func (s StaticInputStreams) list(context.Context, *s2.BasinClient) ([]string, error) { //nolint:unparam
	return s.Streams, nil
}

type PrefixedInputStreams struct {
	Prefix string
}

func (p PrefixedInputStreams) list(ctx context.Context, client *s2.BasinClient) ([]string, error) {
	var (
		hasMore    = true
		streams    []string
		startAfter string
	)

	for hasMore {
		list, err := client.ListStreams(ctx, &s2.ListStreamsRequest{Prefix: p.Prefix, StartAfter: startAfter})
		if err != nil {
			return nil, err
		}

		for _, stream := range list.Streams {
			if stream.DeletedAt != nil {
				// The stream is deleted.
				continue
			}

			streams = append(streams, stream.Name)
			startAfter = stream.Name
		}

		hasMore = list.HasMore
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
	Stream string
	Batch  *s2.SequencedRecordBatch
	AckFunc
	Err error
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
