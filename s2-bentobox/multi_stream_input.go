package s2bentobox

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type MultiStreamInput struct {
	inputStream          <-chan recvOutput
	streamsManagerCloser <-chan struct{}
	cancelSession        context.CancelFunc
}

func ConnectMultiStreamInput(ctx context.Context, config *InputConfig) (*MultiStreamInput, error) {
	client, err := newBasinClient(config.Config)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)

	streamsManagerCloser := make(chan struct{})
	inputStream := make(chan recvOutput, config.MaxInFlight)

	go streamsManager(sessionCtx, client, config, inputStream, streamsManagerCloser)

	return &MultiStreamInput{
		inputStream:          inputStream,
		streamsManagerCloser: streamsManagerCloser,
		cancelSession:        cancelSession,
	}, nil
}

func (msi *MultiStreamInput) ReadBatch(ctx context.Context) (*s2.SequencedRecordBatch, AckFunc, Stream, error) {
	select {
	case r := <-msi.inputStream:
		return r.Batch, r.AckFunc, r.Stream, r.Err

	case <-msi.streamsManagerCloser:
		return nil, nil, "", ErrInputClosed

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
	client *s2.BasinClient,
	config *InputConfig,
	inputStream chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	updateStreamsInterval := time.Minute
	if config.UpdateStreamsInterval != 0 {
		updateStreamsInterval = config.UpdateStreamsInterval
	}

	backoffDuration := 100 * time.Millisecond
	if config.BackoffDuration != 0 {
		backoffDuration = config.BackoffDuration
	}

	updateList := make(chan struct{}, 1)
	updateList <- struct{}{}

	existingWorkers := make(map[string]streamWorker)

	cache := newSeqNumCache(config.Cache)

	spawnWorker := func(stream string) {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		workerCloser := make(chan struct{})
		worker := streamWorker{
			cancel: cancelWorker,
			closer: workerCloser,
		}
		existingWorkers[stream] = worker

		go streamSource(
			workerCtx,
			client,
			config.Logger,
			cache,
			stream,
			config.MaxInFlight,
			backoffDuration,
			config.StartSeqNum,
			inputStream,
			workerCloser,
		)
	}

managerLoop:
	for {
		select {
		case <-ctx.Done():
			break managerLoop

		case <-time.After(updateStreamsInterval):
		case <-updateList:
		}

		// Clear the update list notifier.
		select {
		case <-updateList:
		default:
		}

		newStreams, err := config.Streams.list(ctx, client)
		if err != nil {
			config.Logger.
				With("error", err).
				Error("Failed to list streams")

			// Try updating the update list notifier. We need a retry here.
			select {
			case updateList <- struct{}{}:
			default:
			}

			continue
		}

		newStreamsSet := make(map[string]struct{})
		for _, stream := range newStreams {
			newStreamsSet[stream] = struct{}{}

			if _, found := existingWorkers[stream]; !found {
				config.Logger.With("stream", stream).Info("Reading from S2 source")
				spawnWorker(stream)
			}
		}

		for stream, worker := range existingWorkers {
			if _, found := newStreamsSet[stream]; !found {
				// Cancel the worker that's not in the new list.
				config.Logger.With("stream", stream).Warn("Not reading from S2 source anymore")
				worker.Close() // Don't need to wait here.
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

func streamSource(
	ctx context.Context,
	client *s2.BasinClient,
	logger Logger,
	cache *seqNumCache,
	stream string,
	maxInflight int,
	backoffDuration time.Duration,
	inputStartSeqNum InputStartSeqNum,
	inputStream chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	backoff := make(<-chan time.Time)

	for {
		select {
		case <-ctx.Done():
			return
		case <-backoff:
		default:
		}

		// Never receive (reset)
		backoff = make(<-chan time.Time)

		input, err := connectStreamInput(ctx, client, cache, logger, stream, maxInflight, inputStartSeqNum)
		if err != nil {
			logger.With("error", err, "stream", stream).Error("Failed to connect, retrying.")

			jitter := time.Duration(rand.Int64N(int64(10 * time.Millisecond)))
			backoff = time.After(backoffDuration + jitter)

			continue
		}

		streamSourceRecvLoop(ctx, input, stream, inputStream, logger)
	}
}

func streamSourceRecvLoop(
	ctx context.Context,
	input *streamInput,
	stream string,
	inputStream chan<- recvOutput,
	logger Logger,
) {
	defer input.Close(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		batch, aFn, err := input.ReadBatch(ctx)
		if err != nil {
			if errors.Is(err, ErrInputClosed) {
				logger.With("stream", stream).Debug("Restarting source")

				return
			}

			select {
			case inputStream <- recvOutput{Stream: stream, Err: err}:
			case <-ctx.Done():
				return
			}

			continue
		}

		select {
		case inputStream <- recvOutput{Stream: stream, Batch: batch, AckFunc: aFn}:
		case <-ctx.Done():
			return
		}
	}
}
