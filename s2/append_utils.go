package s2

import (
	"context"
	"encoding/binary"
	"fmt"
)

const defaultMaxInflightBytes = 10 * 1024 * 1024 // 10 MiB

// Represents a pending batch submission to an [AppendSession].
// Call [SubmitFuture.Wait] to block until the batch is accepted.
type SubmitFuture struct {
	ticketCh <-chan *BatchSubmitTicket
	errCh    <-chan error
}

// Blocks until the batch is accepted by the [AppendSession] and returns
// a [BatchSubmitTicket] that can be used to wait for the append acknowledgment.
func (f *SubmitFuture) Wait(ctx context.Context) (*BatchSubmitTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case ticket := <-f.ticketCh:
		return ticket, nil
	case err := <-f.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Resolves with the [AppendAck] once the batch is durable.
type BatchSubmitTicket struct {
	ackCh <-chan *inflightResult
}

// Resolves [BatchSubmitTicket] with the [AppendAck] once the batch is durable.
func (t *BatchSubmitTicket) Ack(ctx context.Context) (*AppendAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case outcome, ok := <-t.ackCh:
		if !ok || outcome == nil {
			return nil, fmt.Errorf("batch submit ticket resolved without a payload")
		}
		return outcome.ack, outcome.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

func applyAppendSessionDefaults(opts *AppendSessionOptions, baseRetry *RetryConfig) *AppendSessionOptions {
	if opts == nil {
		opts = &AppendSessionOptions{}
	}

	if opts.MaxInflightBytes == 0 {
		opts.MaxInflightBytes = defaultMaxInflightBytes
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

	if effective.MaxAttempts <= 0 {
		effective.MaxAttempts = defaultMaxAttempts
	}
	if effective.MinBaseDelay <= 0 {
		effective.MinBaseDelay = defaultMinBaseDelay
	}
	if effective.MaxBaseDelay <= 0 {
		effective.MaxBaseDelay = defaultMaxBaseDelay
	}
	if effective.AppendRetryPolicy == "" {
		effective.AppendRetryPolicy = AppendRetryPolicyAll
	}

	cfgCopy := effective
	opts.RetryConfig = &cfgCopy

	return opts
}
