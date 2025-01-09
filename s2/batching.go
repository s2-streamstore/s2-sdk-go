package s2

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Batching errors.
var (
	ErrAppendRecordSenderClosed = errors.New("append record sender closed")
	ErrAppendRecordSenderExited = errors.New("append record sender unexpectedly exited")
	ErrInvalidNumRecordsInBatch = errors.New("records in a batch must be more than 0 and less than 1000")
	ErrRecordTooBig             = errors.New("record to big to send given batch size limitations")
)

type appendRecordBatchingConfig struct {
	MatchSeqNum     *uint64
	FencingToken    []byte
	LingerDuration  time.Duration
	MaxBatchRecords uint
	MaxBatchBytes   uint
}

// Options to configure batching scheme for AppendRecordBatchingSender.
type AppendRecordBatchingConfigParam interface {
	apply(*appendRecordBatchingConfig) error
}

type applyAppendRecordBatchingConfigParamFunc func(*appendRecordBatchingConfig) error

func (f applyAppendRecordBatchingConfigParamFunc) apply(cc *appendRecordBatchingConfig) error {
	return f(cc)
}

// Enforce that the sequence number issued to the first record matches.
//
// This is incremented automatically for each batch.
func WithMatchSeqNum(matchSeqNum uint64) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.MatchSeqNum = &matchSeqNum

		return nil
	})
}

// Enforce a fencing token.
func WithFencingToken(fencingToken []byte) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.FencingToken = fencingToken

		return nil
	})
}

// Linger duration for records before flushing.
//
// A linger duration of 5ms is set by default. Set to 0 to disable.
func WithLinger(duration time.Duration) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		arbc.LingerDuration = duration

		return nil
	})
}

// Maximum number of records in a batch.
func WithMaxBatchRecords(n uint) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		if n == 0 || n > MaxBatchRecords {
			return ErrInvalidNumRecordsInBatch
		}

		arbc.MaxBatchRecords = n

		return nil
	})
}

type sendRecord struct {
	Record AppendRecord
	Ack    chan<- struct{}
}

// An AppendRecord sender based on AppendInput sender returned by an AppendSession request.
//
// Smartly batch records together based on based on size limits and linger duration.
// The sender enforces the provided fencing token (if any) and auto-increments matching sequence number for
// concurrency control.
type AppendRecordBatchingSender struct {
	sendCh            chan<- *sendRecord
	closeWorkerCancel func()
	workerExitCtx     context.Context
}

// Create a new batching sender for AppendRecords.
//
// Note that this spawns a background worker which must be exited gracefully via the `Close` method.
func NewAppendRecordBatchingSender(
	batchSender Sender[*AppendInput],
	params ...AppendRecordBatchingConfigParam,
) (*AppendRecordBatchingSender, error) {
	config := appendRecordBatchingConfig{
		MatchSeqNum:     nil,
		FencingToken:    nil,
		LingerDuration:  5 * time.Millisecond,
		MaxBatchRecords: MaxBatchRecords,
		MaxBatchBytes:   MaxBatchBytes,
	}

	for _, param := range params {
		if err := param.apply(&config); err != nil {
			return nil, err
		}
	}

	sendCh := make(chan *sendRecord, config.MaxBatchRecords)

	closeWorkerCtx, closeWorkerCancel := context.WithCancel(context.Background())
	workerExitCtx, workerExitCancel := context.WithCancelCause(context.Background())

	go func() {
		defer func() {
			if r := recover(); r != nil {
				workerExitCancel(fmt.Errorf("%w: %v", ErrAppendRecordSenderExited, r))
			}
		}()

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

	recordsToFlush := newEmptyAppendRecordBatch(config.MaxBatchRecords, config.MaxBatchBytes)

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

		recordsToFlush = newEmptyAppendRecordBatch(config.MaxBatchRecords, config.MaxBatchBytes)

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

// Send the AppendRecord for batching, and eventually for appending.
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

// Close the sender gracefully.
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
