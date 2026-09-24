package faulttest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"github.com/s2-streamstore/s2-sdk-go/s2"
	"google.golang.org/protobuf/proto"
)

const (
	beforeCommit = "before_commit"
	afterCommit  = "after_commit"
)

type batch struct {
	Records []s2.AppendRecord `json:"records"`
	Fault   string            `json:"fault,omitempty"`
	Match   bool              `json:"match,omitempty"`
}

type trace struct {
	Session bool                 `json:"session"`
	Policy  s2.AppendRetryPolicy `json:"policy"`
	Batches []batch              `json:"batches"`
}

func TestReplay(t *testing.T) {
	lite := newLite(t)
	if path := os.Getenv("S2_FAULT_TRACE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var script trace
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&script); err != nil {
			t.Fatal(err)
		}
		checkReplay(t, lite.endpoint, script)
		return
	}
	for mode := byte(0); mode < 4; mode++ {
		for fault := byte(0); fault < 6; fault++ {
			t.Run(fmt.Sprintf("%d/%d", mode, fault), func(t *testing.T) {
				checkReplay(t, lite.endpoint, makeTrace([]byte{mode, fault, 3, 1, 255, 192}))
			})
		}
	}
}

func TestReplayRejectsCorruption(t *testing.T) {
	lite := newLite(t)
	for _, target := range []string{"ack", "records"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t, lite.endpoint, makeTrace([]byte{0, 0}))
			h.proxy.mu.Lock()
			h.proxy.corrupt = func(message proto.Message) {
				switch m := message.(type) {
				case *pb.AppendAck:
					if target == "ack" {
						m.End.SeqNum++
					}
				case *pb.ReadBatch:
					if target == "records" {
						m.Records[0].Body = []byte("corrupt")
					}
				}
			}
			h.proxy.mu.Unlock()
			if err := h.replay(); err == nil || !strings.Contains(err.Error(), target) {
				t.Fatalf("expected %s violation, got %v", target, err)
			}
		})
	}
}

func TestReplayDeterministic(t *testing.T) {
	lite := newLite(t)
	var history []string
	for range 2 {
		h := newHarness(t, lite.endpoint, makeTrace([]byte{9, 4, 2, 5}))
		if err := h.replay(); err != nil {
			t.Fatal(err)
		}
		h.proxy.mu.Lock()
		got := slices.Clone(h.proxy.wire)
		h.proxy.mu.Unlock()
		if history != nil && !slices.Equal(history, got) {
			t.Fatalf("replay changed history: %v != %v", history, got)
		}
		history = got
	}
}

func makeTrace(data []byte) trace {
	script := trace{
		Session: data[0]&1 != 0, Policy: s2.AppendRetryPolicyAll,
	}
	if data[0]&2 != 0 {
		script.Policy = s2.AppendRetryPolicyNoSideEffects
	}
	for i, value := range data[1:] {
		b := batch{Match: value&1 != 0}
		if i == 0 {
			b.Fault = []string{"", beforeCommit, afterCommit}[value/2%3]
		}
		for j := 0; j <= int(value)%3; j++ {
			b.Records = append(b.Records, s2.AppendRecord{
				Body:    bytes.Repeat([]byte{byte(i), byte(j), value}, 1+int(value)*2),
				Headers: []s2.Header{{Name: []byte("id"), Value: []byte{byte(i), byte(j)}}},
			})
		}
		script.Batches = append(script.Batches, b)
	}
	return script
}

func checkReplay(t *testing.T, endpoint string, script trace) {
	t.Helper()
	if err := script.validate(); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, endpoint, script)
	if err := h.replay(); err != nil {
		h.proxy.mu.Lock()
		defer h.proxy.mu.Unlock()
		t.Fatalf("%v; artifacts: %s; history: %v", err, h.dir, h.proxy.wire)
	}
}

func (s trace) validate() error {
	if s.Policy != s2.AppendRetryPolicyAll && s.Policy != s2.AppendRetryPolicyNoSideEffects {
		return fmt.Errorf("invalid retry policy %q", s.Policy)
	}
	if len(s.Batches) == 0 || len(s.Batches) > 32 {
		return errors.New("invalid trace bounds")
	}
	var count int
	var size uint64
	for _, b := range s.Batches {
		count += len(b.Records)
		if len(b.Records) == 0 || len(b.Records) > 32 {
			return errors.New("batch must contain 1–32 records")
		}
		if b.Fault != "" && b.Fault != beforeCommit && b.Fault != afterCommit {
			return fmt.Errorf("unknown append fault %q", b.Fault)
		}
		for _, r := range b.Records {
			size += recordBytes(s2.SequencedRecord{Body: r.Body, Headers: r.Headers})
			if r.Timestamp != nil || len(r.Body) > 4096 {
				return errors.New("records must omit timestamps and have bodies at most 4096 bytes")
			}
			for _, header := range r.Headers {
				if len(header.Name) == 0 {
					return errors.New("command records are not supported")
				}
			}
		}
	}
	if count > 128 || size > 256*1024 {
		return errors.New("trace exceeds 128 records or 256 KiB")
	}
	return nil
}

func (h *harness) replay() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var expected []s2.SequencedRecord
	for i, b := range h.script.Batches {
		stream := h.stream(i)
		var session *s2.AppendSession
		if h.script.Session {
			var err error
			session, err = stream.AppendSession(ctx, nil)
			if err != nil {
				return err
			}
			defer session.Close()
		}
		input := &s2.AppendInput{Records: slices.Clone(b.Records)}
		for j := range input.Records {
			input.Records[j].Timestamp = s2.Uint64(42)
		}
		start := uint64(len(expected))
		if b.Match {
			input.MatchSeqNum = &start
		}
		attempts, copies := 1, 1
		wantError := b.Fault == afterCommit && (b.Match || h.script.Policy == s2.AppendRetryPolicyNoSideEffects)
		if b.Fault == beforeCommit || (b.Fault == afterCommit && h.script.Policy == s2.AppendRetryPolicyAll) {
			attempts = 2
		}
		if b.Fault == afterCommit && !wantError {
			copies = 2
		}
		for range copies {
			for _, r := range b.Records {
				expected = append(expected, s2.SequencedRecord{SeqNum: uint64(len(expected)), Timestamp: 42, Body: r.Body, Headers: r.Headers})
			}
		}
		ack, err := appendBatch(ctx, stream, session, input)
		if ctx.Err() != nil || (err != nil) != wantError {
			return fmt.Errorf("batch %d: expected error=%t, got %v", i, wantError, err)
		}
		if wantError {
			status := http.StatusServiceUnavailable
			if h.script.Policy == s2.AppendRetryPolicyAll {
				status = http.StatusPreconditionFailed
			}
			var apiErr *s2.S2Error
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				return fmt.Errorf("batch %d: expected status %d, got %v", i, status, err)
			}
		}
		h.proxy.mu.Lock()
		gotAttempts := h.proxy.attempts[i]
		h.proxy.mu.Unlock()
		if gotAttempts != attempts {
			return fmt.Errorf("batch %d: attempts=%d, want %d", i, gotAttempts, attempts)
		}
		if !wantError {
			end := uint64(len(expected))
			want := s2.AppendAck{Start: s2.StreamPosition{SeqNum: end - uint64(len(b.Records)), Timestamp: 42}, End: s2.StreamPosition{SeqNum: end, Timestamp: 42}, Tail: s2.StreamPosition{SeqNum: end, Timestamp: 42}}
			if ack == nil || *ack != want {
				return fmt.Errorf("batch %d: ack=%+v, want %+v", i, ack, want)
			}
			if session != nil && (session.LastAckedPosition() == nil || *session.LastAckedPosition() != want) {
				return fmt.Errorf("batch %d: last acknowledged position differs from ack", i)
			}
		}
		if session != nil {
			if err := session.Close(); err != nil {
				return err
			}
		}
		if wantError && session != nil {
			break
		}
	}
	got, err := h.stream(len(h.script.Batches)).Read(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(0)})
	if err != nil {
		return err
	}
	if !sameRecords(got.Records, expected) {
		return errors.New("committed records differ from model")
	}
	tail, err := h.stream(len(h.script.Batches)).CheckTail(ctx)
	if err != nil || tail.Tail.SeqNum != uint64(len(expected)) {
		return fmt.Errorf("tail=%+v, err=%v, want %d", tail, err, len(expected))
	}
	return nil
}

func appendBatch(ctx context.Context, stream *s2.StreamClient, session *s2.AppendSession, input *s2.AppendInput) (*s2.AppendAck, error) {
	if session == nil {
		return stream.Append(ctx, input)
	}
	future, err := session.Submit(input)
	if err != nil {
		return nil, err
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		return nil, err
	}
	return ticket.Ack(ctx)
}

func sameRecords(a, b []s2.SequencedRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i, r := range a {
		want := b[i]
		if r.SeqNum != want.SeqNum || r.Timestamp != want.Timestamp || !bytes.Equal(r.Body, want.Body) || len(r.Headers) != len(want.Headers) {
			return false
		}
		for j, header := range r.Headers {
			if !bytes.Equal(header.Name, want.Headers[j].Name) || !bytes.Equal(header.Value, want.Headers[j].Value) {
				return false
			}
		}
	}
	return true
}

func recordBytes(r s2.SequencedRecord) uint64 {
	n := 8 + len(r.Body) + 2*len(r.Headers)
	for _, header := range r.Headers {
		n += len(header.Name) + len(header.Value)
	}
	return uint64(n)
}
