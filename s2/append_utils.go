package s2

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	defaultMaxInflightBytes = 5 * 1024 * 1024 // 5 MiB
	minInflightBytes        = 1 * 1024 * 1024 // 1 MiB
)

// Represents a pending batch submission to an [AppendSession].
// Call [SubmitFuture.Wait] to block until the batch is accepted.
type SubmitFuture struct {
	ticketCh <-chan *BatchSubmitTicket
	errCh    <-chan error
	// state decides whether a canceled Wait or the session settles the batch.
	state      atomic.Int32
	cancelWait context.CancelFunc
	// withdrawn is closed once a canceled Wait withdraws the batch.
	withdrawnOnce sync.Once
	withdrawn     chan struct{}
	withdrawErr   error
}

func (f *SubmitFuture) withdrawnCh() chan struct{} {
	f.withdrawnOnce.Do(func() { f.withdrawn = make(chan struct{}) })
	return f.withdrawn
}

const (
	submitPending int32 = iota
	submitSettled
	submitWithdrawn
)

// Blocks until the batch is accepted by the [AppendSession] and returns
// a [BatchSubmitTicket] that can be used to wait for the append acknowledgment.
//
// If ctx ends first, the batch is withdrawn and never appended, and every
// Wait returns ctx's error. If the session already settled the batch, Wait
// returns that result instead.
func (f *SubmitFuture) Wait(ctx context.Context) (*BatchSubmitTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	withdrawn := f.withdrawnCh()
	select {
	case ticket := <-f.ticketCh:
		return ticket, nil
	case err := <-f.errCh:
		return nil, err
	case <-withdrawn:
		return nil, f.withdrawErr
	case <-ctx.Done():
	}
	// Spec A4c: a canceled submit accepts nothing.
	if f.state.CompareAndSwap(submitPending, submitWithdrawn) {
		f.withdrawErr = ctx.Err()
		close(withdrawn)
		if f.cancelWait != nil {
			f.cancelWait()
		}
		return nil, f.withdrawErr
	}
	select {
	case ticket := <-f.ticketCh:
		return ticket, nil
	case err := <-f.errCh:
		return nil, err
	case <-withdrawn:
		return nil, f.withdrawErr
	}
}

// Resolves with the [AppendAck] once the batch is durable.
type BatchSubmitTicket struct {
	ackCh   <-chan *inflightResult
	outcome ticketOutcome[inflightResult]
}

// Resolves [BatchSubmitTicket] with the [AppendAck] once the batch is durable.
// Every call returns the same outcome.
func (t *BatchSubmitTicket) Ack(ctx context.Context) (*AppendAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	outcome, err := t.outcome.await(ctx, t.ackCh)
	if err != nil {
		return nil, err
	}
	if outcome == nil {
		return nil, fmt.Errorf("batch submit ticket resolved without a payload")
	}
	return outcome.ack, outcome.err
}

// ticketOutcome keeps a ticket's outcome once received, so every await returns
// it (spec A6). The sender sends one value and then closes the channel.
type ticketOutcome[T any] struct {
	once    sync.Once
	settled chan struct{}
	value   *T
}

func (o *ticketOutcome[T]) await(ctx context.Context, ch <-chan *T) (*T, error) {
	o.once.Do(func() { o.settled = make(chan struct{}) })
	select {
	case <-o.settled:
	case value, ok := <-ch:
		if ok {
			o.value = value
			close(o.settled)
			break
		}
		// Another await received the outcome; wait until it is recorded.
		select {
		case <-o.settled:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return o.value, nil
}

// Create a new Fence command record.
func NewFenceCommandRecord(token string, timestamp *uint64) AppendRecord {
	record := AppendRecord{
		Headers: []Header{{Name: nil, Value: []byte("fence")}},
		Body:    []byte(token),
	}
	if timestamp != nil {
		ts := *timestamp
		record.Timestamp = &ts
	}
	return record
}

// Create a new Trim command record.
func NewTrimCommandRecord(seqNum uint64, timestamp *uint64) AppendRecord {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, seqNum)
	record := AppendRecord{
		Headers: []Header{{Name: nil, Value: []byte("trim")}},
		Body:    buf,
	}
	if timestamp != nil {
		ts := *timestamp
		record.Timestamp = &ts
	}
	return record
}

// Calculates the metered size in bytes of an append record.
func MeteredPayloadBytes(record AppendRecord) uint64 {
	numHeaders := len(record.Headers)
	headersSize := 0
	for _, header := range record.Headers {
		headersSize += len(header.Name) + len(header.Value)
	}

	bodySize := len(record.Body)
	return uint64(8 + 2*numHeaders + headersSize + bodySize)
}

// Calculates the metered size in bytes of a sequenced record.
func MeteredSequencedRecordBytes(record SequencedRecord) uint64 {
	numHeaders := len(record.Headers)
	headersSize := 0
	for _, header := range record.Headers {
		headersSize += len(header.Name) + len(header.Value)
	}

	bodySize := len(record.Body)
	return uint64(8 + 2*numHeaders + headersSize + bodySize)
}

// Calculates the total metered size in bytes of a batch of append records.
func MeteredBatchBytes(records []AppendRecord) int64 {
	var total uint64
	for _, record := range records {
		total += MeteredPayloadBytes(record)
	}
	return int64(total)
}

func applyAppendSessionDefaults(opts *AppendSessionOptions, baseRetry *RetryConfig) (*AppendSessionOptions, error) {
	if opts == nil {
		opts = &AppendSessionOptions{}
	} else {
		clone := *opts
		clone.StreamConfig = cloneStreamConfigPtr(opts.StreamConfig)
		opts = &clone
	}

	if opts.MaxInflightBytes == 0 {
		opts.MaxInflightBytes = defaultMaxInflightBytes
	} else if opts.MaxInflightBytes < minInflightBytes {
		return nil, newValidationError(fmt.Sprintf("maxInflightBytes must be at least %d", minInflightBytes))
	}

	var effective RetryConfig
	switch {
	case baseRetry != nil:
		effective = *baseRetry
	case DefaultRetryConfig != nil:
		effective = *DefaultRetryConfig
	default:
		effective = *DefaultRetryConfig
	}

	if userCfg := opts.RetryConfig; userCfg != nil {
		if userCfg.MaxAttempts > 0 {
			effective.MaxAttempts = userCfg.MaxAttempts
		}
		if userCfg.MinBaseDelay > 0 {
			effective.MinBaseDelay = userCfg.MinBaseDelay
		}
		if userCfg.MaxBaseDelay > 0 {
			effective.MaxBaseDelay = userCfg.MaxBaseDelay
		}
		if userCfg.AppendRetryPolicy != "" {
			effective.AppendRetryPolicy = userCfg.AppendRetryPolicy
		}
	}

	opts.RetryConfig = withRetryDefaults(&effective)

	return opts, nil
}
