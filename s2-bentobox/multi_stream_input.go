package bentobox

import (
	"context"
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

func (msi *MultiStreamInput) ReadBatch(ctx context.Context) (*RecordBatch, AckFunc, Stream, error) {
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
			basin,
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

		// Clear the update list notifier
		select {
		case <-updateList:
		default:
		}

		newStreams, err := config.Streams.list(ctx, basin)
		if err != nil {
			if config.Logger != nil {
				config.Logger.Error("failed to list streams", "error", err)
			}

			// Try updating the update list notifier for retry
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
				if config.Logger != nil {
					config.Logger.Info("reading from S2 source", "stream", stream)
				}
				spawnWorker(stream)
			}
		}

		for stream, worker := range existingWorkers {
			if _, found := newStreamsSet[stream]; !found {
				if config.Logger != nil {
					config.Logger.Warn("not reading from S2 source anymore", "stream", stream)
				}
				worker.Close()
				delete(existingWorkers, stream)
			}
		}
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
	basin *s2.BasinClient,
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

	var backoff <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-backoff:
		default:
		}

		// Reset backoff
		backoff = nil

		input, err := connectStreamInput(ctx, basin, cache, logger, stream, maxInflight, inputStartSeqNum)
		if err != nil {
			if logger != nil {
				logger.Error("failed to connect, retrying", "error", err, "stream", stream)
			}

			jitter := time.Duration(rand.Int63n(int64(10 * time.Millisecond)))
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
			if err == ErrInputClosed {
				if logger != nil {
					logger.Debug("restarting source", "stream", stream)
				}
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
		case inputStream <- recvOutput{Stream: stream, Batch: &RecordBatch{Records: batch}, AckFunc: aFn}:
		case <-ctx.Done():
			return
		}
	}
}
