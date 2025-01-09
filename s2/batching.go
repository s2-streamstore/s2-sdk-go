package s2

import (
	"context"
	"errors"
	"time"
)

// Errors.
var (
	ErrAppendRecordSenderClosed = errors.New("append record sender closed")
	ErrInvalidNumRecordsInBatch = errors.New("records in a batch must be more than 0 and less than 1000")
	ErrRecordTooBig             = errors.New("record to big to send given batch size limitations")
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
		if n == 0 || n > MaxBatchRecords {
			return ErrInvalidNumRecordsInBatch
		}

		arbc.MaxRecordsInBatch = n

		return nil
	})
}

type sendRecord struct {
	Record AppendRecord
	Ack    chan<- struct{}
}

type AppendRecordBatchingSender struct {
	sendCh            chan<- *sendRecord
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
		MaxRecordsInBatch: MaxBatchRecords,
		MaxBatchBytes:     MaxBatchBytes,
	}

	for _, param := range params {
		if err := param.apply(&config); err != nil {
			return nil, err
		}
	}

	sendCh := make(chan *sendRecord, config.MaxRecordsInBatch)

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
	sendCh <-chan *sendRecord,
	sender Sender[*AppendInput],
	config *appendRecordBatchingConfig,
) error {
	var peekedRecord *AppendRecord

	recordsToFlush, _ := newAppendRecordBatch(config.MaxRecordsInBatch, config.MaxBatchBytes)

	flush := func() error {
		if recordsToFlush.IsEmpty() {
			if peekedRecord != nil {
				return ErrRecordTooBig
			}

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
			*config.MatchSeqNum += uint64(recordsToFlush.Len())
		}

		input := &AppendInput{
			Records:      recordsToFlush,
			MatchSeqNum:  matchSeqNum,
			FencingToken: config.FencingToken,
		}

		if err := sender.Send(input); err != nil {
			return err
		}

		recordsToFlush, _ = newAppendRecordBatch(config.MaxRecordsInBatch, config.MaxBatchBytes)

		if peekedRecord != nil {
			if ok := recordsToFlush.Append(*peekedRecord); !ok {
				return ErrRecordTooBig
			}

			peekedRecord = nil
		}

		return nil
	}

	var lingerCh <-chan time.Time

	for {
		if recordsToFlush.IsEmpty() {
			lingerCh = make(<-chan time.Time) // Never
		} else if recordsToFlush.Len() == 1 {
			lingerCh = time.After(config.LingerDuration)
		}

		select {
		case record := <-sendCh:
			if ok := recordsToFlush.Append(record.Record); !ok {
				if peekedRecord != nil {
					panic("peeked record cannot be occupied if this is the first time limit has reached")
				}

				peekedRecord = &record.Record
			}

			if recordsToFlush.IsFull() || peekedRecord != nil {
				if err := flush(); err != nil {
					return err
				}
			}

			// Acknowledge that the record has been appended.
			close(record.Ack)

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
	ack := make(chan struct{})

	select {
	case s.sendCh <- &sendRecord{
		Record: record,
		Ack:    ack,
	}: // Sent.

	case <-s.workerExitCtx.Done():
		return context.Cause(s.workerExitCtx)
	}

	select {
	case <-ack:
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
