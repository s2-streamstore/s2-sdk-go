package faulttest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
	"github.com/zeebo/xxh3"
)

const verificationRevision = "b4af8c8ef4965d9b335101c422eadb33f3169004"

type historyEvent struct {
	Event    map[string]any `json:"event"`
	ClientID int            `json:"client_id"`
	OpID     int            `json:"op_id"`
}

type history struct {
	mu       sync.Mutex
	events   []historyEvent
	deferred []historyEvent
}

func (h *history) start(id int, input any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, historyEvent{Event: map[string]any{"Start": input}, ClientID: id, OpID: id})
}

func (h *history) finish(id int, result any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	event := historyEvent{Event: map[string]any{"Finish": result}, ClientID: id, OpID: id}
	// Unknown appends remain pending until the history ends, as in the Rust collector.
	if result == "AppendIndefiniteFailure" {
		h.deferred = append(h.deferred, event)
	} else {
		h.events = append(h.events, event)
	}
}

func (h *history) jsonl() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, events := range [][]historyEvent{h.events, h.deferred} {
		for _, event := range events {
			_ = encoder.Encode(event)
		}
	}
	return buf.Bytes()
}

func appendStart(input *s2.AppendInput) any {
	hashes := make([]uint64, len(input.Records))
	var token *string
	for i, record := range input.Records {
		hashes[i] = xxh3.Hash(record.Body)
		if len(record.Headers) == 1 && len(record.Headers[0].Name) == 0 && string(record.Headers[0].Value) == fenceCommand {
			token = s2.String(string(record.Body))
		}
	}
	return map[string]any{"Append": map[string]any{
		"num_records": len(hashes), "record_hashes": hashes,
		"set_fencing_token": token, "fencing_token": input.FencingToken, "match_seq_num": input.MatchSeqNum,
	}}
}

func appendFinish(ack *s2.AppendAck, err error) any {
	if err == nil {
		return map[string]any{"AppendSuccess": map[string]uint64{"tail": ack.End.SeqNum}}
	}
	var apiErr *s2.S2Error
	if errors.As(err, &apiErr) && (apiErr.HasNoSideEffects() || apiErr.Status == http.StatusPreconditionFailed || (apiErr.Origin == "sdk" && apiErr.Code == "VALIDATION")) {
		return "AppendDefiniteFailure"
	}
	return "AppendIndefiniteFailure"
}

func streamHash(records []s2.SequencedRecord) uint64 {
	var hash uint64
	var buf [8]byte
	for _, record := range records {
		binary.LittleEndian.PutUint64(buf[:], xxh3.Hash(record.Body))
		hash = xxh3.HashSeed(buf[:], hash)
	}
	return hash
}

func checkerBinary(t testing.TB) string {
	t.Helper()
	path := os.Getenv("S2_PORCUPINE")
	if path == "" {
		path, _ = exec.LookPath("s2-porcupine")
	}
	if path == "" {
		t.Skip("set S2_PORCUPINE to the s2-verification checker at " + verificationRevision)
	}
	path, err := exec.LookPath(path)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func checkHistory(binary, dir string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-file=history.jsonl")
	cmd.Dir = dir
	if err := os.WriteFile(filepath.Join(dir, "history.jsonl"), data, 0600); err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Porcupine: %w: %s", err, out)
	}
	if !bytes.Contains(out, []byte("passed: is linearizable")) {
		return fmt.Errorf("unexpected checker output: %s", out)
	}
	return nil
}

func TestHistoryHashVectors(t *testing.T) {
	records := make([]s2.SequencedRecord, 0, 3)
	for i, body := range []string{"foo", "bar", "baz"} {
		records = append(records, s2.SequencedRecord{Body: []byte(body)})
		want := []uint64{0x4d2b003ee417c3a5, 0x132e5d5dd7936edd, 0x732ee99abc5002ff}[i]
		if got := streamHash(records); got != want {
			t.Fatalf("shared hash vector %d: got %x, want %x", i, got, want)
		}
	}
}

func TestHistoryChecker(t *testing.T) {
	binary := checkerBinary(t)
	for _, outcome := range []string{"acknowledged", "uncertain", "lost", "corrupt", "definite"} {
		t.Run(outcome, func(t *testing.T) {
			h := &history{}
			input := &s2.AppendInput{Records: []s2.AppendRecord{{Body: []byte("foo")}}}
			h.start(0, appendStart(input))
			switch outcome {
			case "uncertain":
				h.finish(0, appendFinish(nil, &s2.S2Error{Origin: "sdk", Code: "REQUEST_TIMEOUT", Status: http.StatusRequestTimeout}))
			case "definite":
				h.finish(0, appendFinish(nil, &s2.S2Error{Origin: "server", Code: "rate_limited", Status: http.StatusTooManyRequests}))
			default:
				h.finish(0, map[string]any{"AppendSuccess": map[string]uint64{"tail": 1}})
			}
			records := []s2.SequencedRecord{{Body: []byte("foo")}}
			switch outcome {
			case "lost":
				records = nil
			case "corrupt":
				records[0].Body = []byte("bar")
			}
			h.start(1, "Read")
			h.finish(1, map[string]any{"ReadSuccess": map[string]uint64{"tail": uint64(len(records)), "stream_hash": streamHash(records)}})
			err := checkHistory(binary, resultDir(t), h.jsonl())
			valid := outcome == "acknowledged" || outcome == "uncertain"
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && (err == nil || !strings.Contains(err.Error(), "NOT linearizable")) {
				t.Fatalf("checker accepted invalid history or failed to check it: %v", err)
			}
		})
	}
}
