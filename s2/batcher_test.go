package s2

import (
	"context"
	"testing"
	"time"
)

func TestBatcher_FlushOnMaxRecords(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    2,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := batcher.Add(AppendRecord{Body: []byte("b")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}

	first := <-batcher.Batches()
	if got := len(first.Input.Records); got != 2 {
		t.Fatalf("expected 2 records, got %d", got)
	}

	if err := batcher.Add(AppendRecord{Body: []byte("c")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batcher.Close()

	second := <-batcher.Batches()
	if got := len(second.Input.Records); got != 1 {
		t.Fatalf("expected 1 record, got %d", got)
	}
}

func TestBatcher_FlushOnMaxBytes(t *testing.T) {
	ctx := context.Background()
	rec := AppendRecord{Body: []byte("a")}
	size := MeteredPayloadBytes(rec)
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxMeteredBytes: size*2 - 1,
		Linger:          time.Hour,
		ChannelBuffer:   10,
	})

	if err := batcher.Add(rec, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := batcher.Add(rec, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batcher.Close()

	first := <-batcher.Batches()
	second := <-batcher.Batches()

	if len(first.Input.Records) != 1 || len(second.Input.Records) != 1 {
		t.Fatalf("expected two single-record batches, got %d and %d", len(first.Input.Records), len(second.Input.Records))
	}
}

func TestBatcher_LingerFlush(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		Linger:        20 * time.Millisecond,
		MaxRecords:    100,
		ChannelBuffer: 10,
	})

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}

	select {
	case batch := <-batcher.Batches():
		if len(batch.Input.Records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(batch.Input.Records))
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected batch to flush after linger")
	}
	batcher.Close()
}

func TestBatcher_CloseFlushesRemaining(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		Linger:        time.Hour,
		MaxRecords:    100,
		ChannelBuffer: 10,
	})

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batcher.Close()

	batch := <-batcher.Batches()
	if len(batch.Input.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(batch.Input.Records))
	}
}

func TestBatcher_OversizedRecordRejected(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxMeteredBytes: 10,
	})

	err := batcher.Add(AppendRecord{Body: make([]byte, 100)}, nil)
	if err == nil {
		t.Fatalf("expected error for oversized record")
	}
}

func TestBatcher_FencingTokenPropagation(t *testing.T) {
	ctx := context.Background()
	token := "fence"
	batcher := NewBatcher(ctx, &BatchingOptions{
		FencingToken: &token,
		MaxRecords:   1,
		Linger:       time.Hour,
	})

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batch := <-batcher.Batches()
	if batch.Input.FencingToken == nil || *batch.Input.FencingToken != token {
		t.Fatalf("expected fencing token %q, got %v", token, batch.Input.FencingToken)
	}
	batcher.Close()
}

func TestBatcher_MatchSeqNumAutoIncrements(t *testing.T) {
	ctx := context.Background()
	start := uint64(0)
	batcher := NewBatcher(ctx, &BatchingOptions{
		MatchSeqNum:   &start,
		MaxRecords:    2,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := batcher.Add(AppendRecord{Body: []byte("b")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := batcher.Add(AppendRecord{Body: []byte("c")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batcher.Close()

	first := <-batcher.Batches()
	second := <-batcher.Batches()

	if first.Input.MatchSeqNum == nil || *first.Input.MatchSeqNum != 0 {
		t.Fatalf("expected first match_seq_num=0, got %v", first.Input.MatchSeqNum)
	}
	if second.Input.MatchSeqNum == nil || *second.Input.MatchSeqNum != 2 {
		t.Fatalf("expected second match_seq_num=2, got %v", second.Input.MatchSeqNum)
	}
}

func TestBatcher_InvalidConfigClamped(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:      MaxBatchRecords + 10,
		MaxMeteredBytes: MaxBatchMeteredBytes + 10,
	})

	if batcher.opts.MaxRecords != MaxBatchRecords {
		t.Fatalf("expected max records clamped to %d, got %d", MaxBatchRecords, batcher.opts.MaxRecords)
	}
	if batcher.opts.MaxMeteredBytes != MaxBatchMeteredBytes {
		t.Fatalf("expected max bytes clamped to %d, got %d", MaxBatchMeteredBytes, batcher.opts.MaxMeteredBytes)
	}
}

func TestBatcher_PipeToAppendSession(t *testing.T) {
	ctx := context.Background()
	start := uint64(7)
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    2,
		Linger:        time.Hour,
		ChannelBuffer: 10,
		MatchSeqNum:   &start,
	})
	session := &fakeAppendSession{}

	if err := batcher.Add(AppendRecord{Body: []byte("a")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := batcher.Add(AppendRecord{Body: []byte("b")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	first := <-batcher.Batches()
	if first.Input.MatchSeqNum == nil || *first.Input.MatchSeqNum != 7 {
		t.Fatalf("expected first match_seq_num=7, got %v", first.Input.MatchSeqNum)
	}

	if err := batcher.Add(AppendRecord{Body: []byte("c")}, nil); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	batcher.Close()

	second := <-batcher.Batches()
	if second.Input.MatchSeqNum == nil || *second.Input.MatchSeqNum != 9 {
		t.Fatalf("expected second match_seq_num=9, got %v", second.Input.MatchSeqNum)
	}

	submitAndAck := func(input *AppendInput) *AppendAck {
		future, err := session.Submit(input)
		if err != nil {
			t.Fatalf("submit failed: %v", err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatalf("wait failed: %v", err)
		}
		ack, err := ticket.Ack(ctx)
		if err != nil {
			t.Fatalf("ack failed: %v", err)
		}
		return ack
	}

	ack1 := submitAndAck(first.Input)
	ack2 := submitAndAck(second.Input)
	if ack1.End.SeqNum-ack1.Start.SeqNum != 2 {
		t.Fatalf("expected first ack count 2, got %d", ack1.End.SeqNum-ack1.Start.SeqNum)
	}
	if ack2.Start.SeqNum != ack1.End.SeqNum {
		t.Fatalf("expected second ack to start at %d, got %d", ack1.End.SeqNum, ack2.Start.SeqNum)
	}
}
