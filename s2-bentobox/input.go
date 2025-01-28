package s2bentobox

import (
	"context"
	"errors"
	"sync"
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/s2"
	"github.com/tidwall/btree"
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

type InputConfig struct {
	*Config
	Streams               InputStreams
	MaxInFlight           int
	UpdateStreamsInterval time.Duration
	Cache                 SeqNumCache
	Logger                Logger
}

type recvOutput struct {
	Stream string
	Batch  *s2.SequencedRecordBatch
	Err    error
}

type Input struct {
	config               *InputConfig
	inputStream          chan recvOutput
	streamsManagerCloser <-chan struct{}
	cancelSession        context.CancelFunc

	cache *seqNumCache

	ackMu sync.Mutex
	nacks map[string]*btree.Set[uint64]
}

func ConnectInput(ctx context.Context, config *InputConfig) (*Input, error) {
	client, err := newBasinClient(config.Config)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)

	streamsManagerCloser := make(chan struct{})
	// Twice the inflight requests since we can have this blocked by nacks otherwise.
	// TODO: Maybe this number can be improved somehow?
	inputStream := make(chan recvOutput, config.MaxInFlight*2)

	cache := newSeqNumCache(config.Cache)

	go streamsManagerWorker(
		sessionCtx,
		client,
		config,
		cache,
		inputStream,
		streamsManagerCloser,
	)

	return &Input{
		config:               config,
		inputStream:          inputStream,
		streamsManagerCloser: streamsManagerCloser,
		cancelSession:        cancelSession,
		cache:                cache,
		nacks:                make(map[string]*btree.Set[uint64]),
	}, nil
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

func streamsManagerWorker(
	ctx context.Context,
	client *s2.BasinClient,
	config *InputConfig,
	cache *seqNumCache,
	inputStream chan<- recvOutput,
	streamsManagerCloser chan<- struct{},
) {
	defer close(streamsManagerCloser)

	updateStreamsInterval := time.Minute
	if config.UpdateStreamsInterval != 0 {
		updateStreamsInterval = config.UpdateStreamsInterval
	}

	var (
		existingWorkers    = make(map[string]streamWorker)
		ticker             = time.NewTicker(updateStreamsInterval)
		updateListNotifier = make(chan struct{}, 1)
	)

	defer ticker.Stop()

	// Fetch the list once immediately at startup.
	updateListNotifier <- struct{}{}

	spawnWorker := func(stream string) {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		workerCloser := make(chan struct{})
		worker := streamWorker{
			cancel: cancelWorker,
			closer: workerCloser,
		}
		existingWorkers[stream] = worker

		go receiverWorker(
			workerCtx,
			client,
			config,
			cache,
			stream,
			inputStream,
			workerCloser,
		)
	}

outerLoop:
	for {
		select {
		case <-ctx.Done():
			break outerLoop

		case <-updateListNotifier:
		case <-ticker.C:
		}

		// Clear the update list notifier.
		select {
		case <-updateListNotifier:
		default:
		}

		newStreams, err := config.Streams.list(ctx, client)
		if err != nil {
			config.Logger.
				With("error", err).
				Error("Failed to list streams")

			// Try updating the update list notifier. We need a retry here.
			select {
			case updateListNotifier <- struct{}{}:
			default:
			}

			continue
		}

		newStreamsSet := make(map[string]struct{})
		for _, stream := range newStreams {
			newStreamsSet[stream] = struct{}{}

			if _, found := existingWorkers[stream]; !found {
				spawnWorker(stream)
			}
		}

		for stream, worker := range existingWorkers {
			if _, found := newStreamsSet[stream]; !found {
				// Cancel the worker that's not in the new list.
				config.Logger.With("stream", stream).Warn("Not reading from S2 source anymore")
				worker.Close()
				delete(existingWorkers, stream)
			}
		}
	}

	// Close and wait for all to exit.
	for _, worker := range existingWorkers {
		worker.Close()
		worker.Wait()
	}
}

func receiverWorker(
	ctx context.Context,
	client *s2.BasinClient,
	config *InputConfig,
	cache *seqNumCache,
	stream string,
	recvCh chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	config.Logger.With("stream", stream).Info("Reading from S2 source")
	defer config.Logger.With("stream", stream).Debug("Exiting S2 source worker")

	var (
		receiver       s2.Receiver[s2.ReadOutput]
		backoff        time.Duration
		startSeqNumOpt *uint64
	)

	initReceiver := func() error {
		select {
		case <-time.After(backoff):
			backoff = time.Second

		case <-ctx.Done():
			return ctx.Err()
		}

		startSeqNum := optr.UnwrapOr(startSeqNumOpt, func() uint64 {
			startSeqNum, err := cache.Get(ctx, stream)
			if err != nil {
				config.Logger.
					With(
						"error", err,
						"stream", stream,
					).
					Warn("Failed to get last sequence number from cache")

				// Set it to 0 and let the loop try and get the latest sequence number.
				return 0
			}

			return startSeqNum
		})

		var err error

		receiver, err = client.StreamClient(stream).ReadSession(ctx, &s2.ReadSessionRequest{
			StartSeqNum: startSeqNum,
		})
		if err != nil {
			return err
		}

		// Reset so we can hit cache the next time unless otherwise asked to.
		startSeqNumOpt = nil

		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if receiver == nil {
			if err := initReceiver(); err != nil {
				config.Logger.
					With(
						"error", err,
						"stream", stream,
					).
					Error("Failed to initialize receiver")

				continue
			}
		}

		output, err := receiver.Recv()
		if err != nil {
			select {
			case recvCh <- recvOutput{Stream: stream, Err: err}:
				// Reset the receiver.
				receiver = nil

				continue
			case <-ctx.Done():
				return
			}
		}

		switch o := output.(type) {
		case s2.ReadOutputBatch:
			select {
			case recvCh <- recvOutput{Stream: stream, Batch: o.SequencedRecordBatch}:
				// OK
			case <-ctx.Done():
				return
			}

		case s2.ReadOutputFirstSeqNum:
			startSeqNumOpt = optr.Some(uint64(o))
			receiver = nil

		case s2.ReadOutputNextSeqNum:
			startSeqNumOpt = optr.Some(uint64(o))
			receiver = nil
		}
	}
}

func (i *Input) ReadBatch(ctx context.Context) (*s2.SequencedRecordBatch, string, error) {
	select {
	case output := <-i.inputStream:
		if output.Err != nil {
			return nil, output.Stream, output.Err
		}

		return output.Batch, output.Stream, nil

	case <-ctx.Done():
		return nil, "", ctx.Err()

	case <-i.streamsManagerCloser:
		if err := waitForClosers(ctx, i.streamsManagerCloser); err != nil {
			return nil, "", err
		}

		return nil, "", ErrInputClosed
	}
}

func (i *Input) AckFunc(stream string, batch *s2.SequencedRecordBatch) func(context.Context, error) error {
	return func(ctx context.Context, err error) error {
		i.config.Logger.With("stream", stream).Debug("Acknowledging batch")

		if len(batch.Records) == 0 {
			// What is even being acked?
			return nil
		}

		if err == nil {
			// Set the cache if nacks are empty.
			return func() error {
				i.ackMu.Lock()
				defer i.ackMu.Unlock()

				streamNacks, ok := i.nacks[stream]
				if ok && streamNacks.Len() > 0 {
					// If smallest stream nack > this batch seq num:
					// then -> we've already considered this acknowledged.
					// else -> this message might have been received again or can be.
					streamNacks.Delete(batch.Records[0].SeqNum)

					return nil
				}

				// Ack this message by storing in the cache.
				lastSeqNum := batch.Records[len(batch.Records)-1].SeqNum
				nextSeqNum := lastSeqNum + 1

				// Only cache the value if it's greater than the value that's already cached,
				// otherwise this could result in duplication of messages.
				//
				// Duplication is not an error but we can try our best not to.
				cachedSeqNum, cErr := i.cache.Get(ctx, stream)
				if cErr != nil || cachedSeqNum < nextSeqNum {
					return i.cache.Set(ctx, stream, nextSeqNum)
				}

				return nil
			}()
		}

		if cErr := func() error {
			i.ackMu.Lock()
			defer i.ackMu.Unlock()

			streamNacks, ok := i.nacks[stream]
			if !ok {
				streamNacks = &btree.Set[uint64]{}
				i.nacks[stream] = streamNacks
			}

			nackSeqNum := batch.Records[0].SeqNum

			streamNacks.Insert(nackSeqNum)

			// Since it's already in the stream nacks, check if it's the minima.
			if minSeqNum, ok := streamNacks.Min(); ok && minSeqNum == nackSeqNum {
				// Update the cache since it's the minima.
				return i.cache.Set(ctx, stream, nackSeqNum)
			}

			return nil
		}(); cErr != nil {
			return cErr
		}

		// Send the batch again to be acknowledged.
		select {
		case i.inputStream <- recvOutput{
			Stream: stream,
			Batch:  batch,
		}:
			return nil

		// Ack functions can also be called during closures.
		// Error if the input has closed.
		case <-i.streamsManagerCloser:
			return context.Canceled

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (i *Input) Close(ctx context.Context) error {
	i.cancelSession()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-i.streamsManagerCloser:
		return waitForClosers(ctx, i.streamsManagerCloser)
	}
}
