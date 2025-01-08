package s2

import (
	"context"
	"errors"
	"time"
)

const mibBytes uint = 1024 * 1024

// Errors.
var (
	ErrAppendRecordSenderClosed = errors.New("append record sender closed")
	ErrInvalidNumRecordsInBatch = errors.New("records in a batch must be more than 0 and less than 1000")
)

type appendRecordBatchingConfig struct {
	MatchSeqNum       *uint64
	FencingToken      []byte
	LingerDuration    time.Duration
	MaxRecordsInBatch uint
	MaxBatchBytes     uint
}

type AppendRecordBatchingConfigParam interface {
	apply(*appendRecordBatchingConfig) error
}

type applyAppendRecordBatchingConfigParamFunc func(*appendRecordBatchingConfig) error

func (f applyAppendRecordBatchingConfigParamFunc) apply(cc *appendRecordBatchingConfig) error {
	return f(cc)
}

func WithMatchSeqNum(matchSeqNum uint64) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.MatchSeqNum = &matchSeqNum

		return nil
	})
}

func WithFencingToken(fencingToken []byte) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.FencingToken = fencingToken

		return nil
	})
}

func WithLinger(duration time.Duration) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.LingerDuration = duration

		return nil
	})
}

func WithMaxRecordsInBatch(n uint) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		if n == 0 || n > 1000 {
			return ErrInvalidNumRecordsInBatch
		}

		arbc.MaxRecordsInBatch = n

		return nil
	})
}

type AppendRecordBatchingSender struct {
	sendCh            chan<- AppendRecord
	closeWorkerCancel func()
	workerExitCtx     context.Context
}

func NewAppendRecordBatchingSender(
	batchSender Sender[*AppendInput],
	params ...AppendRecordBatchingConfigParam,
) (*AppendRecordBatchingSender, error) {
	config := appendRecordBatchingConfig{
		MatchSeqNum:       nil,
		FencingToken:      nil,
		LingerDuration:    5 * time.Millisecond,
		MaxRecordsInBatch: 1000,
		MaxBatchBytes:     mibBytes,
	}

	for _, param := range params {
		if err := param.apply(&config); err != nil {
			return nil, err
		}
	}

	sendCh := make(chan AppendRecord)

	closeWorkerCtx, closeWorkerCancel := context.WithCancel(context.Background())
	workerExitCtx, workerExitCancel := context.WithCancelCause(context.Background())

	go func() {
		if err := appendRecordBatchingWorker(closeWorkerCtx, sendCh, batchSender, &config); err != nil {
			workerExitCancel(err)
		} else {
			workerExitCancel(ErrAppendRecordSenderClosed)
		}
	}()

	return &AppendRecordBatchingSender{
		sendCh:            sendCh,
		closeWorkerCancel: closeWorkerCancel,
		workerExitCtx:     workerExitCtx,
	}, nil
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
		lingerCh := make(<-chan time.Time) // Never
		if len(recordsToFlush) > 0 {
			// Initialize waiting for linger only if there's atleast one record.
			// Otherwise there's really no point.
			lingerCh = time.After(config.LingerDuration)
		}

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

		case <-lingerCh:
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
		return context.Cause(s.workerExitCtx)
	}
}

func (s *AppendRecordBatchingSender) Close() error {
	s.closeWorkerCancel()
	<-s.workerExitCtx.Done()

	if err := context.Cause(s.workerExitCtx); err != nil {
		if errors.Is(err, ErrAppendRecordSenderClosed) {
			return nil
		}

		return err
	}

	return nil
}
