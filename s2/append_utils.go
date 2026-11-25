package s2

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
)

const (
	defaultMaxInflightBytes = 5 * 1024 * 1024 // 5 MiB
	minInflightBytes        = 1 * 1024 * 1024 // 1 MiB
)

// Represents a pending batch submission to an [AppendSession].
// Call [SubmitFuture.Wait] to block until the batch is accepted.
type SubmitFuture struct {
	done chan struct{}
	// cancelWait stops the session's capacity wait for a withdrawn batch.
	cancelWait context.CancelFunc

	mu     sync.Mutex
	state  submitState
	ticket *BatchSubmitTicket
	err    error
}

type submitState uint8

const (
	submitPending submitState = iota
	// submitAccepting means the session reserved capacity and is queuing the
	// batch, so it can no longer be withdrawn.
	submitAccepting
	submitResolved
)

func newSubmitFuture(cancelWait context.CancelFunc) *SubmitFuture {
	return &SubmitFuture{done: make(chan struct{}), cancelWait: cancelWait}
}

// Blocks until the batch is accepted by the [AppendSession] and returns
// a [BatchSubmitTicket] that can be used to wait for the append acknowledgment.
//
// When ctx ends, Wait tries to withdraw the batch. If withdrawal wins,
// the batch will not be appended, and this and later calls to Wait return
// ctx's error. If acceptance starts first, Wait returns the submission
// result instead, even if ctx has already ended.
func (f *SubmitFuture) Wait(ctx context.Context) (*BatchSubmitTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-f.done:
	case <-ctx.Done():
		f.withdraw(ctx.Err())
		<-f.done
	}
	return f.ticket, f.err
}

// withdraw resolves a batch that is not yet accepted with cause, so the
// session skips it (spec A4c: a canceled submit accepts nothing).
func (f *SubmitFuture) withdraw(cause error) {
	f.mu.Lock()
	withdrawn := f.state == submitPending
	if withdrawn {
		f.resolveLocked(nil, cause)
	}
	f.mu.Unlock()
	if withdrawn && f.cancelWait != nil {
		f.cancelWait()
	}
}

// beginAccept claims the batch for acceptance, reporting false if it was
// withdrawn or already resolved.
func (f *SubmitFuture) beginAccept() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != submitPending {
		return false
	}
	f.state = submitAccepting
	return true
}

func (f *SubmitFuture) accept(ticket *BatchSubmitTicket) { f.resolve(ticket, nil) }

func (f *SubmitFuture) fail(err error) { f.resolve(nil, err) }

// resolve settles the future unless it is already settled (e.g. withdrawn).
func (f *SubmitFuture) resolve(ticket *BatchSubmitTicket, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != submitResolved {
		f.resolveLocked(ticket, err)
	}
}

func (f *SubmitFuture) resolveLocked(ticket *BatchSubmitTicket, err error) {
	f.state = submitResolved
	f.ticket = ticket
	f.err = err
	close(f.done)
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

	return opts, nil
}
