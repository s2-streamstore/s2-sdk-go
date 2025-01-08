package s2

import (
	"context"
	"errors"
	"time"
)

var ErrAppendRecordSenderClosed = errors.New("append record sender closed")

type appendRecordBatchingConfig struct {
	MatchSeqNum       *uint64
	FencingToken      []byte
	LingerDuration    time.Duration
	MaxRecordsInBatch uint
	// TODO: Metered Size
}

type AppendRecordBatchingSender struct {
	sendCh            chan<- AppendRecord
	closeWorkerCancel func()
	workerExitCtx     context.Context
}

func NewAppendRecordBatchingSender(batchSender Sender[*AppendInput]) *AppendRecordBatchingSender {
	// TODO: Config.
	var config appendRecordBatchingConfig

	sendCh := make(chan AppendRecord)

	closeWorkerCtx, closeWorkerCancel := context.WithCancel(context.Background())
	workerExitCtx, workerExitCancel := context.WithCancelCause(context.Background())

	go func() {
		workerExitCancel(
			appendRecordBatchingWorker(closeWorkerCtx, sendCh, batchSender, &config),
		)
	}()

	return &AppendRecordBatchingSender{
		sendCh:            sendCh,
		closeWorkerCancel: closeWorkerCancel,
		workerExitCtx:     workerExitCtx,
	}
}

func appendRecordBatchingWorker(
	ctx context.Context,
	sendCh <-chan AppendRecord,
	sender Sender[*AppendInput],
	config *appendRecordBatchingConfig,
) error {
	var peekedRecord *AppendRecord
	recordsToFlush := make([]AppendRecord, 0, config.MaxRecordsInBatch)

	flush := func() error {
		if len(recordsToFlush) == 0 {
			return nil
		}

		var matchSeqNum *uint64
		if config.MatchSeqNum != nil {
			// Copy current match sequence number in another variable so we can
			// mutate it later for the up-coming sequence number. Working with
			// optionals as pointer in Go is hard.
			currentMatchSeqNum := *config.MatchSeqNum
			matchSeqNum = &currentMatchSeqNum
			// Update the next matching sequence number.
			*config.MatchSeqNum += uint64(len(recordsToFlush))
		}

		input := &AppendInput{
			Records:      recordsToFlush,
			MatchSeqNum:  matchSeqNum,
			FencingToken: config.FencingToken,
		}

		if err := sender.Send(input); err != nil {
			return err
		}

		// Only clear after it's sent.
		clear(recordsToFlush)
		if peekedRecord != nil {
			recordsToFlush = append(recordsToFlush, *peekedRecord)
			peekedRecord = nil
		}

		return nil
	}

	for {
		select {
		case record := <-sendCh:
			if uint(len(recordsToFlush)) < config.MaxRecordsInBatch {
				recordsToFlush = append(recordsToFlush, record)
			} else {
				if peekedRecord != nil {
					panic("peeked record cannot be occupied if this is the first time limit has reached")
				}

				peekedRecord = &record
			}

			if uint(len(recordsToFlush)) == config.MaxRecordsInBatch || peekedRecord != nil {
				if err := flush(); err != nil {
					return err
				}
			}

		case <-time.After(config.LingerDuration):
			if err := flush(); err != nil {
				return err
			}

		case <-ctx.Done():
			if err := flush(); err != nil {
				return err
			}

			return nil
		}
	}
}

func (s *AppendRecordBatchingSender) Send(record AppendRecord) error {
	select {
	case s.sendCh <- record:
		return nil
	case <-s.workerExitCtx.Done():
		if err := context.Cause(s.workerExitCtx); err != nil {
			return err
		} else {
			return ErrAppendRecordSenderClosed
		}
	}
}

func (s *AppendRecordBatchingSender) Close() error {
	s.closeWorkerCancel()
	<-s.workerExitCtx.Done()
	return context.Cause(s.workerExitCtx)
}
