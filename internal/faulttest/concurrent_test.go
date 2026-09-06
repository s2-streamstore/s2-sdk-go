package faulttest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const (
	opAppend         = "append"
	opSession        = "session"
	opProducer       = "producer"
	opRead           = "read"
	opTail           = "tail"
	readReset        = "read_reset"
	crashAfterCommit = "crash_after_commit"
	fenceCommand     = "fence"
)

type operation struct {
	Kind  string          `json:"kind"`
	Input *s2.AppendInput `json:"input,omitempty"`
	Fault string          `json:"fault,omitempty"`
}

type concurrentTrace struct {
	Waves       [][]operation      `json:"waves"`
	Compression s2.CompressionType `json:"compression,omitempty"`
}

type observedAppend struct {
	input            *s2.AppendInput
	start, end, tail uint64
}

func testClient(endpoint string, compression s2.CompressionType) *s2.Client {
	token := os.Getenv("S2_FAULT_ACCESS_TOKEN")
	if token == "" {
		token = "test"
	}
	return s2.New(token, &s2.ClientOptions{
		BaseURL: endpoint, MakeBasinBaseURL: func(string) string { return endpoint },
		Compression: compression, RequestTimeout: 3 * time.Second,
		RetryConfig: &s2.RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: s2.AppendRetryPolicyNoSideEffects},
	})
}

func concurrentScenario(seed byte) concurrentTrace {
	script := concurrentTrace{Compression: s2.CompressionType(seed % 3)}
	for wave := range 6 {
		ops := make([]operation, 0, 5)
		for writer := range 3 {
			body := bytes.Repeat([]byte(fmt.Sprintf("seed=%d/wave=%d/writer=%d", seed, wave, writer)), 64)
			input := &s2.AppendInput{Records: []s2.AppendRecord{{Body: body, Headers: []s2.Header{{Name: []byte("writer"), Value: []byte{byte(writer)}}}}}}
			kind := []string{opAppend, opSession, opProducer}[writer]
			switch wave {
			case 1:
				input.MatchSeqNum = s2.Uint64(3)
			case 2:
				input.Records = []s2.AppendRecord{s2.NewFenceCommandRecord(fmt.Sprintf("fence-%d", writer), nil)}
				input.MatchSeqNum = s2.Uint64(4)
			case 3:
				input.FencingToken = s2.String(fmt.Sprintf("fence-%d", writer))
			}
			fault := ""
			if wave == 4 {
				fault = []string{afterCommit, beforeCommit, ""}[writer]
			}
			ops = append(ops, operation{Kind: kind, Input: input, Fault: fault})
		}
		ops = append(ops, operation{Kind: opRead}, operation{Kind: opTail})
		if wave == 5 {
			ops[3].Fault = readReset
		}
		script.Waves = append(script.Waves, ops)
	}
	return script
}

func (s concurrentTrace) validate() error {
	if len(s.Waves) == 0 || len(s.Waves) > 32 || s.Compression > s2.CompressionGzip {
		return fmt.Errorf("invalid concurrent trace bounds")
	}
	for _, wave := range s.Waves {
		if len(wave) == 0 || len(wave) > 8 {
			return fmt.Errorf("waves must contain 1–8 operations")
		}
		for _, op := range wave {
			switch op.Kind {
			case opAppend, opSession, opProducer:
				if op.Input == nil || len(op.Input.Records) != 1 {
					return fmt.Errorf("concurrent append operations require one record")
				}
				if op.Fault != "" && op.Fault != beforeCommit && op.Fault != afterCommit && op.Fault != crashAfterCommit {
					return fmt.Errorf("invalid append fault %q", op.Fault)
				}
			case opRead, opTail:
				if op.Input != nil || (op.Fault != "" && (op.Kind != opRead || op.Fault != readReset)) {
					return fmt.Errorf("invalid read/tail operation")
				}
			default:
				return fmt.Errorf("unknown operation %q", op.Kind)
			}
		}
	}
	return nil
}

func TestConcurrent(t *testing.T) {
	checker := checkerBinary(t)
	if script := loadConcurrentTrace(t); script != nil {
		runConcurrentMock(t, checker, *script)
		return
	}
	for seed := byte(0); seed < 3; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runConcurrentMock(t, checker, concurrentScenario(seed)) })
	}
}

func loadConcurrentTrace(t *testing.T) *concurrentTrace {
	t.Helper()
	path := os.Getenv("S2_CONCURRENT_TRACE")
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var script concurrentTrace
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&script); err != nil {
		t.Fatal(err)
	}
	return &script
}

func FuzzConcurrent(f *testing.F) {
	checker := checkerBinary(f)
	f.Add([]byte{0, 1, 2})
	f.Add([]byte{2, 3, 4, 5})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > 8 {
			return
		}
		script := concurrentScenario(data[0])
		script.Waves = script.Waves[:1]
		for i, value := range data {
			wave := []operation{{Kind: opRead}, {Kind: opTail}}
			for writer := range 2 {
				input := &s2.AppendInput{Records: []s2.AppendRecord{{Body: []byte{0, byte(i), byte(writer), value}}}}
				if i > 0 {
					if value&8 != 0 {
						input.MatchSeqNum = s2.Uint64(uint64(3 + 2*i))
					}
					if value&16 != 0 {
						input.Records = []s2.AppendRecord{s2.NewFenceCommandRecord(fmt.Sprintf("fuzz-%d", writer), nil)}
					}
					if value&32 != 0 {
						input.FencingToken = s2.String(fmt.Sprintf("fuzz-%d", writer))
					}
				}
				wave = append(wave, operation{Kind: []string{opAppend, opSession, opProducer}[int(value+byte(writer))%3],
					Input: input})
			}
			if i == 0 {
				wave[2].Fault = []string{"", beforeCommit, afterCommit}[value%3]
				wave[0].Fault = readReset
			}
			script.Waves = append(script.Waves, wave)
		}
		runConcurrentMock(t, checker, script)
	})
}

func runConcurrentMock(t *testing.T, checker string, script concurrentTrace) {
	t.Helper()
	h := newHarness(t, trace{Policy: s2.AppendRetryPolicyNoSideEffects})
	h.delivered = nil
	runConcurrent(t, checker, h.endpoint, "test", "test", script, nil, nil)
}

func runConcurrent(t *testing.T, checker, endpoint, basin, streamName string, script concurrentTrace, crash func() error, afterWave func() error) {
	t.Helper()
	if err := script.validate(); err != nil {
		t.Fatal(err)
	}
	dir := resultDir(t)
	data, err := json.MarshalIndent(script, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trace.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p := newFaultProxy(t, endpoint, script, crash)
	h := &history{}
	defer func() {
		_ = os.WriteFile(filepath.Join(dir, "history.jsonl"), h.jsonl(), 0600)
		p.mu.Lock()
		defer p.mu.Unlock()
		wire, _ := json.MarshalIndent(p.wire, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "wire.json"), wire, 0600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var acknowledged []observedAppend
	var resultMu sync.Mutex
	var violations []error
	id := 0
	for _, wave := range script.Waves {
		var wg sync.WaitGroup
		start := make(chan struct{})
		crashing := false
		for _, op := range wave {
			if op.Fault == crashAfterCommit {
				if crash == nil {
					t.Fatal("crash_after_commit requires a managed Lite process")
				}
				crashing = true
			}
		}
		for _, op := range wave {
			opID := id
			id++
			var input any = "CheckTail"
			if op.Input != nil {
				input = appendStart(op.Input)
			} else if op.Kind == opRead {
				input = "Read"
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				h.start(opID, input)
				client := testClient(fmt.Sprintf("%s/op/%d/v1", p.endpoint, opID), script.Compression).Basin(basin).Stream(s2.StreamName(streamName))
				var violation error
				switch op.Kind {
				case opAppend, opSession, opProducer:
					ack, err := performAppend(ctx, client, op)
					h.finish(opID, appendFinish(ack, err))
					if err == nil && (op.Fault == afterCommit || op.Fault == crashAfterCommit) {
						violation = fmt.Errorf("op %d: append succeeded despite a withheld ACK", opID)
					}
					if !crashing && op.Fault != afterCommit && err != nil {
						var apiErr *s2.S2Error
						guarded := op.Input.MatchSeqNum != nil || op.Input.FencingToken != nil
						if !guarded || !errors.As(err, &apiErr) || apiErr.Status != http.StatusPreconditionFailed {
							violation = fmt.Errorf("op %d: unexpected append failure: %w", opID, err)
						}
					}
					if err == nil {
						if ack.End.SeqNum != ack.Start.SeqNum+uint64(len(op.Input.Records)) || ack.Tail.SeqNum < ack.End.SeqNum {
							violation = fmt.Errorf("op %d: invalid ack %+v", opID, ack)
						}
						resultMu.Lock()
						acknowledged = append(acknowledged, observedAppend{input: op.Input, start: ack.Start.SeqNum, end: ack.End.SeqNum, tail: ack.Tail.SeqNum})
						resultMu.Unlock()
					}
				case opRead:
					records, err := readPrefix(ctx, client, p.readSeen[opID])
					if err != nil {
						h.finish(opID, "ReadFailure")
						if !crashing {
							violation = fmt.Errorf("op %d: read failed: %w", opID, err)
						}
					} else {
						h.finish(opID, map[string]any{"ReadSuccess": map[string]uint64{"tail": uint64(len(records)), "stream_hash": streamHash(records)}})
						violation = contiguous(records)
					}
				case opTail:
					tail, err := client.CheckTail(ctx)
					if err != nil {
						h.finish(opID, "CheckTailFailure")
						if !crashing {
							violation = fmt.Errorf("op %d: tail failed: %w", opID, err)
						}
					} else {
						h.finish(opID, map[string]any{"CheckTailSuccess": map[string]uint64{"tail": tail.Tail.SeqNum}})
					}
				}
				if violation != nil {
					resultMu.Lock()
					violations = append(violations, violation)
					resultMu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		if afterWave != nil {
			if err := afterWave(); err != nil {
				t.Fatal(err)
			}
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	client := testClient(endpoint, script.Compression).Basin(basin).Stream(s2.StreamName(streamName))
	h.start(id, "Read")
	records, err := readPrefix(ctx, client, nil)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	h.finish(id, map[string]any{"ReadSuccess": map[string]uint64{"tail": uint64(len(records)), "stream_hash": streamHash(records)}})
	if err := contiguous(records); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	acknowledged = append(acknowledged, p.committed...)
	p.mu.Unlock()
	for _, observed := range acknowledged {
		if observed.end != observed.start+uint64(len(observed.input.Records)) || observed.tail < observed.end || observed.tail > uint64(len(records)) {
			t.Fatalf("invalid or lost acknowledged range: [%d,%d), tail=%d, records=%d", observed.start, observed.end, observed.tail, len(records))
		}
		for i, input := range observed.input.Records {
			got := records[observed.start+uint64(i)]
			want := s2.SequencedRecord{SeqNum: got.SeqNum, Timestamp: got.Timestamp, Headers: input.Headers, Body: input.Body}
			if !sameRecords([]s2.SequencedRecord{got}, []s2.SequencedRecord{want}) {
				t.Fatalf("ack maps to different payload at %d", got.SeqNum)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatal(violations)
	}
	p.mu.Lock()
	for id, op := range p.ops {
		if op.Fault != "" && !p.fired[id] {
			t.Errorf("op %d: fault %s was not injected", id, op.Fault)
		}
	}
	p.mu.Unlock()
	if err := checkHistory(checker, dir, h.jsonl()); err != nil {
		t.Fatalf("%v; artifacts: %s", err, dir)
	}
}

func performAppend(ctx context.Context, stream *s2.StreamClient, op operation) (*s2.AppendAck, error) {
	if op.Kind == opAppend {
		return stream.Append(ctx, op.Input)
	}
	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if op.Kind == opSession {
		return appendBatch(ctx, stream, session, op.Input)
	}
	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{MaxRecords: 1, MatchSeqNum: op.Input.MatchSeqNum, FencingToken: op.Input.FencingToken})
	producer := s2.NewProducer(ctx, batcher, session)
	defer producer.Close()
	future, err := producer.Submit(op.Input.Records[0])
	if err != nil {
		return nil, err
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if err := producer.Flush(ctx); err != nil {
		return nil, err
	}
	ack, err := ticket.Ack(ctx)
	if err != nil {
		return nil, err
	}
	return ack.BatchAppendAck(), nil
}

func readPrefix(ctx context.Context, stream *s2.StreamClient, seen chan<- struct{}) ([]s2.SequencedRecord, error) {
	reader, err := stream.ReadSession(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(0), Count: s2.Uint64(math.MaxUint64), Wait: s2.Int32(0)})
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var records []s2.SequencedRecord
	for reader.Next() {
		records = append(records, reader.Record())
		select {
		case seen <- struct{}{}:
		default:
		}
	}
	if ctx.Err() != nil {
		return records, ctx.Err()
	}
	err = reader.Err()
	var rangeErr *s2.RangeNotSatisfiableError
	if errors.As(err, &rangeErr) && rangeErr.Tail != nil && rangeErr.Tail.SeqNum == uint64(len(records)) {
		err = nil
	}
	return records, err
}

func contiguous(records []s2.SequencedRecord) error {
	for i, record := range records {
		if record.SeqNum != uint64(i) {
			return fmt.Errorf("non-contiguous read: record %d has sequence %d", i, record.SeqNum)
		}
	}
	return nil
}
