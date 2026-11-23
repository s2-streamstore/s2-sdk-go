package bentobox

import (
	"context"
	"errors"
	"math/rand"
	"time"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

type MultiStreamInput struct {
	inputStream          <-chan recvOutput
	streamsManagerCloser <-chan struct{}
	cancelSession        context.CancelFunc
}

func ConnectMultiStreamInput(ctx context.Context, config *InputConfig) (*MultiStreamInput, error) {
	if config.Logger == nil {
		cfg := *config
		cfg.Logger = nopLogger{}
		config = &cfg
	}

	client := newClient(config.Config)
	basin := client.Basin(config.Basin)

	sessionCtx, cancelSession := context.WithCancel(ctx)

	streamsManagerCloser := make(chan struct{})
	inputStream := make(chan recvOutput, config.MaxInFlight)

	go streamsManager(sessionCtx, basin, config, inputStream, streamsManagerCloser)

	return &MultiStreamInput{
		inputStream:          inputStream,
		streamsManagerCloser: streamsManagerCloser,
		cancelSession:        cancelSession,
	}, nil
}

func (msi *MultiStreamInput) ReadBatch(ctx context.Context) ([]s2.SequencedRecord, AckFunc, Stream, error) {
	select {
	case r, ok := <-msi.inputStream:
		if !ok {
			return nil, nil, "", ErrInputClosed
		}
		return r.Batch, r.AckFunc, r.Stream, r.Err

	case <-ctx.Done():
		return nil, nil, "", ctx.Err()
	}
}

func (msi *MultiStreamInput) Close(ctx context.Context) error {
	msi.cancelSession()

	select {
	case <-msi.streamsManagerCloser:
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}

func streamsManager(
	ctx context.Context,
	basin *s2.BasinClient,
	config *InputConfig,
	inputStream chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	updateStreamsInterval := time.Minute
	if config.UpdateStreamsInterval != 0 {
		updateStreamsInterval = config.UpdateStreamsInterval
	}

	listStreamsPageInterval := time.Second
	if config.ListStreamsPageInterval != 0 {
		listStreamsPageInterval = config.ListStreamsPageInterval
	}

	backoffDuration := 100 * time.Millisecond
	if config.BackoffDuration != 0 {
		backoffDuration = config.BackoffDuration
	}

	existingWorkers := make(map[string]streamWorker)

	cache := newSeqNumCache(config.Cache, config.Logger)

	spawnWorker := func(stream string) {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		workerCloser := make(chan struct{})
		worker := streamWorker{
			cancel: cancelWorker,
			closer: workerCloser,
		}
		existingWorkers[stream] = worker

		connect := func(ctx context.Context) (recvBatchSource, error) {
			input, err := connectStreamInput(ctx, basin, cache, config.Logger, stream, config.MaxInFlight, config.StartSeqNum)
			if err != nil {
				return nil, err
			}
			return input, nil
		}

		go streamSource(
			workerCtx,
			connect,
			config.Logger,
			cache,
			stream,
			backoffDuration,
			inputStream,
			workerCloser,
		)
	}

	discovery := newStreamDiscovery(config.Streams)
	var wait time.Duration

managerLoop:
	for {
		if wait > 0 {
			select {
			case <-ctx.Done():
				break managerLoop
			case <-time.After(wait):
			}
		} else if ctx.Err() != nil {
			break managerLoop
		}

		page, complete, err := discovery.next(ctx, basin)
		if err != nil {
			config.Logger.
				With("error", err).
				Error("Failed to list streams")
			wait = listStreamsPageInterval
			continue
		}

		for _, stream := range page {
			if _, found := existingWorkers[stream]; !found {
				config.Logger.With("stream", stream).Info("Reading from S2 source")
				spawnWorker(stream)
			}
		}

		if complete == nil {
			wait = listStreamsPageInterval
			continue
		}

		for stream, worker := range existingWorkers {
			if _, found := complete[stream]; !found {
				config.Logger.With("stream", stream).Warn("Not reading from S2 source anymore")
				worker.Close()
				worker.Wait()
				delete(existingWorkers, stream)
				cache.Forget(stream)
			}
		}

		wait = updateStreamsInterval
	}

	// Close and wait for all workers to exit
	for _, worker := range existingWorkers {
		worker.Close()
		worker.Wait()
	}

	close(inputStream)
}

func streamSource(
	ctx context.Context,
	connect func(context.Context) (recvBatchSource, error),
	logger Logger,
	cache *seqNumCache,
	stream string,
	backoffDuration time.Duration,
	inputStream chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	var backoff <-chan time.Time
	startBackoff := func() {
		jitter := time.Duration(rand.Int63n(int64(10 * time.Millisecond)))
		backoff = time.After(backoffDuration + jitter)
	}

	for {
		if backoff != nil {
			select {
			case <-ctx.Done():
				return
			case <-backoff:
			}
			backoff = nil
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		input, err := connect(ctx)
		if err != nil {
			logger.With("error", err, "stream", stream).Error("Failed to connect, retrying.")
			startBackoff()
			continue
		}

		// Read sessions connect lazily, so failures surface here rather than
		// from connect.
		if failed := streamSourceRecvLoop(ctx, input, cache, stream, inputStream, logger); failed {
			startBackoff()
		}
	}
}

// Surface of *streamInput that streamSourceRecvLoop needs. Defined here so
// tests can drive the loop with a fake reader.
type recvBatchSource interface {
	ReadBatch(ctx context.Context) ([]s2.SequencedRecord, AckFunc, error)
	Close(ctx context.Context) error
}

// Reports whether the session ended on a read error, so the caller backs off
// before reconnecting.
func streamSourceRecvLoop(
	ctx context.Context,
	input recvBatchSource,
	cache *seqNumCache,
	stream string,
	inputStream chan<- recvOutput,
	logger Logger,
) bool {
	defer input.Close(ctx)

	for {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		batch, aFn, err := input.ReadBatch(ctx)
		if err != nil {
			if errors.Is(err, ErrInputClosed) {
				logger.With("stream", stream).Debug("Restarting source")

				return false
			}

			var rangeErr *s2.RangeNotSatisfiableError
			if errors.As(err, &rangeErr) && rangeErr.Tail != nil {
				logger.With("stream", stream, "tail_seq_num", rangeErr.Tail.SeqNum).
					Warn("Cached position beyond stream tail, resetting to tail")
				if setErr := cache.ResetToTail(ctx, stream, rangeErr.Tail.SeqNum); setErr != nil {
					// ResetToTail already updated the in-memory position to the
					// tail, so don't Forget: that would fall back to the stale
					// durable value and tight-loop on 416.
					logger.With("stream", stream, "error", setErr).
						Error("Failed to durably reset cached sequence number after 416")
				}
				return false
			}

			select {
			case inputStream <- recvOutput{Stream: stream, Err: err}:
			case <-ctx.Done():
				return false
			}

			return true
		}

		select {
		case inputStream <- recvOutput{Stream: stream, Batch: batch, AckFunc: aFn}:
		case <-ctx.Done():
			return false
		}
	}
}
