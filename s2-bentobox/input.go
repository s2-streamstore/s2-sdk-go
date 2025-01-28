package s2bentobox

import (
	"context"
	"errors"
	"time"

	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/s2"
)

var ErrInputClosed = errors.New("input closed")

type SeqNumCache interface {
	Get(ctx context.Context, stream string) (uint64, error)
	Set(ctx context.Context, stream string, seqNum uint64) error
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

type restartWorkerParams struct {
	Stream      string
	StartSeqNum *uint64
	Closer      chan<- struct{}
}

type Input struct {
	config               *InputConfig
	recvCh               <-chan recvOutput
	streamsManagerCloser <-chan struct{}
	cancelSession        context.CancelFunc
	restartWorker        chan<- restartWorkerParams
}

func ConnectInput(ctx context.Context, config *InputConfig) (*Input, error) {
	client, err := newBasinClient(config.Config)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)

	streamsManagerCloser := make(chan struct{})
	restartWorker := make(chan restartWorkerParams, config.MaxInFlight)
	recvCh := make(chan recvOutput, config.MaxInFlight)

	go streamsManagerWorker(
		sessionCtx,
		client,
		config,
		recvCh,
		restartWorker,
		streamsManagerCloser,
	)

	return &Input{
		config:               config,
		recvCh:               recvCh,
		streamsManagerCloser: streamsManagerCloser,
		cancelSession:        cancelSession,
		restartWorker:        restartWorker,
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
	recvCh chan<- recvOutput,
	restartWorker <-chan restartWorkerParams,
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

	spawnWorker := func(stream string, startSeqNum *uint64) {
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
			stream,
			startSeqNum,
			recvCh,
			workerCloser,
		)
	}

outerLoop:
	for {
		select {
		case <-ctx.Done():
			break outerLoop

		case params := <-restartWorker:
			if worker, found := existingWorkers[params.Stream]; found {
				worker.Close()
				worker.Wait()
			}

			// Spawn a new worker with updated start sequence number.
			spawnWorker(params.Stream, params.StartSeqNum)
			// Notify that the restart is completed.
			close(params.Closer)

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
				spawnWorker(
					stream,
					/* Get sequence number from cache */ nil,
				)
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
	stream string,
	startSeqNumParam *uint64,
	recvCh chan<- recvOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	config.Logger.With("stream", stream).Info("Reading from S2 source")
	defer config.Logger.With("stream", stream).Debug("Exiting S2 source worker")

	var (
		receiver s2.Receiver[s2.ReadOutput]
		backoff  time.Duration
	)

	updateStartSeqNum := func(s uint64) error {
		startSeqNumParam = optr.Some(s)

		return config.Cache.Set(ctx, stream, s)
	}

	initReceiver := func() error {
		select {
		case <-time.After(backoff):
			backoff = time.Second

		case <-ctx.Done():
			return ctx.Err()
		}

		startSeqNum := optr.UnwrapOr(startSeqNumParam, func() uint64 {
			startSeqNum, err := config.Cache.Get(ctx, stream)
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

		// Set the sequence number in the cache.
		if err := updateStartSeqNum(startSeqNum); err != nil {
			return err
		}

		var err error

		receiver, err = client.StreamClient(stream).ReadSession(ctx, &s2.ReadSessionRequest{
			StartSeqNum: startSeqNum,
		})
		if err != nil {
			return err
		}

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
				if len(o.Records) > 0 {
					lastSeqNum := o.Records[len(o.Records)-1].SeqNum
					nextStartSeqNum := lastSeqNum + 1

					// Be optimistic in marking the message read. Let the "nacks" indicate that
					// the records needs to be re-sent!
					if err := updateStartSeqNum(nextStartSeqNum); err != nil {
						config.Logger.
							With(
								"error", err,
								"stream", stream,
							).
							Error("Failed to set cache value for last sequence number")
					}
				}

			case <-ctx.Done():
				return
			}

		case s2.ReadOutputFirstSeqNum:
			receiver = nil

			if err := updateStartSeqNum(uint64(o)); err != nil {
				config.Logger.
					With(
						"error", err,
						"stream", stream,
					).
					Error("Failed to set cache value for first sequence number")
			}

		case s2.ReadOutputNextSeqNum:
			receiver = nil

			if err := updateStartSeqNum(uint64(o)); err != nil {
				config.Logger.
					With(
						"error", err,
						"stream", stream,
					).
					Error("Failed to set cache value for next sequence number")
			}
		}
	}
}

func (i *Input) ReadBatch(ctx context.Context) (*s2.SequencedRecordBatch, string, error) {
	select {
	case output := <-i.recvCh:
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

		if err == nil {
			return nil
		}

		restartCloser := make(chan struct{})

		var startSeqNum *uint64
		if len(batch.Records) > 0 {
			startSeqNum = optr.Some(batch.Records[0].SeqNum)
		}

		select {
		case i.restartWorker <- restartWorkerParams{
			Stream: stream,
			// Force a start sequence number so the cache doesn't affect where
			// the worker restarts from.
			StartSeqNum: startSeqNum,
			Closer:      restartCloser,
		}:

		// Since acks are tried even after the input has been "cancelled", we need to handle this
		// case explicitly.
		case <-i.streamsManagerCloser:
			// Update the cache for next time explicitly.
			if startSeqNum != nil {
				if cErr := i.config.Cache.Set(ctx, stream, *startSeqNum); cErr != nil {
					return cErr
				}
			}

			return context.Canceled

		case <-ctx.Done():
			return ctx.Err()
		}

		// Wait until restart.
		select {
		case <-restartCloser:
			return nil

		// Same as above.
		case <-i.streamsManagerCloser:
			if startSeqNum != nil {
				if cErr := i.config.Cache.Set(ctx, stream, *startSeqNum); cErr != nil {
					return cErr
				}
			}

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
