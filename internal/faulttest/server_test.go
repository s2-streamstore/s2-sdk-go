package faulttest

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"github.com/s2-streamstore/s2-sdk-go/s2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type harness struct {
	script    trace
	stream    *s2.StreamClient
	delivered chan struct{}
	mu        sync.Mutex
	batch     int
	attempt   int
	reads     int
	log       []*pb.SequencedRecord
	history   []string
	conns     []net.Conn
	corrupt   func(proto.Message)
	fence     string
	endpoint  string
}

func newHarness(t *testing.T, script trace) *harness {
	t.Helper()
	h := &harness{script: script, delivered: make(chan struct{})}
	server := httptest.NewUnstartedServer(h2c.NewHandler(http.HandlerFunc(h.serveHTTP), &http2.Server{}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			h.mu.Lock()
			h.conns = append(h.conns, conn)
			h.mu.Unlock()
		}
	}
	server.Start()
	h.endpoint = server.URL
	t.Cleanup(func() {
		server.Close()
		h.mu.Lock()
		defer h.mu.Unlock()
		// h2c connections are hijacked and are not closed by httptest.Server.
		for _, conn := range h.conns {
			_ = conn.Close()
		}
	})
	client := s2.New("test", &s2.ClientOptions{
		BaseURL:          server.URL,
		MakeBasinBaseURL: func(string) string { return server.URL },
		Compression:      script.Compression,
		RetryConfig:      &s2.RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: script.Policy},
	})
	h.stream = client.Basin("test").Stream("test")
	return h
}

func (h *harness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	streaming := r.Header.Get("Content-Type") == "s2s/proto"
	switch {
	case r.Method == http.MethodPost:
		h.serveAppend(w, r, streaming)
	case strings.HasSuffix(r.URL.Path, "/tail"):
		h.mu.Lock()
		tail := len(h.log)
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s2.TailResponse{Tail: s2.StreamPosition{SeqNum: uint64(tail), Timestamp: 42}})
	case r.Method == http.MethodGet:
		h.serveRead(w, r, streaming)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *harness) serveAppend(w http.ResponseWriter, r *http.Request, streaming bool) {
	defer r.Body.Close()
	if streaming {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}
	frames := framing.NewFrameReader(r.Body)
	for {
		var data []byte
		var err error
		if streaming {
			var frame *framing.S2SFrame
			frame, err = frames.ReadFrame()
			if err == nil {
				data, err = frame.DecompressedBody()
			}
		} else {
			data, err = io.ReadAll(r.Body)
			if err == nil {
				data, err = framing.Decompress(data, framing.ParseContentEncoding(r.Header.Get("Content-Encoding")))
			}
		}
		if err != nil {
			return
		}
		var input pb.AppendInput
		if err := proto.Unmarshal(data, &input); err != nil {
			h.replyError(w, streaming, http.StatusBadRequest, "invalid")
			return
		}
		h.mu.Lock()
		h.attempt++
		fault := ""
		if h.attempt == 1 && len(h.script.Batches) > 0 {
			fault = h.script.Batches[h.batch].Fault
		}
		h.history = append(h.history, fmt.Sprintf("append batch=%d attempt=%d fault=%s", h.batch, h.attempt, fault))
		if fault == beforeCommit {
			h.mu.Unlock()
			h.replyError(w, streaming, http.StatusTooManyRequests, "rate_limited")
			return
		}
		start := uint64(len(h.log))
		if input.MatchSeqNum != nil && *input.MatchSeqNum != start {
			h.mu.Unlock()
			h.replyError(w, streaming, http.StatusPreconditionFailed, "seq_num_mismatch")
			return
		}
		if input.FencingToken != nil && *input.FencingToken != h.fence {
			h.mu.Unlock()
			h.replyError(w, streaming, http.StatusPreconditionFailed, "fencing_token_mismatch")
			return
		}
		for _, record := range input.Records {
			if len(record.Headers) == 1 && len(record.Headers[0].Name) == 0 && string(record.Headers[0].Value) == fenceCommand {
				h.fence = string(record.Body)
			}
			h.log = append(h.log, &pb.SequencedRecord{SeqNum: uint64(len(h.log)), Timestamp: 42, Body: record.Body, Headers: record.Headers})
		}
		end := uint64(len(h.log))
		h.history = append(h.history, fmt.Sprintf("commit [%d,%d)", start, end))
		h.mu.Unlock()
		if fault == afterCommit {
			h.replyError(w, streaming, http.StatusServiceUnavailable, "unavailable")
			return
		}
		ack := &pb.AppendAck{Start: &pb.StreamPosition{SeqNum: start, Timestamp: 42}, End: &pb.StreamPosition{SeqNum: end, Timestamp: 42}, Tail: &pb.StreamPosition{SeqNum: end, Timestamp: 42}}
		if !h.replyProto(w, ack, streaming) || !streaming {
			return
		}
	}
}

func (h *harness) serveRead(w http.ResponseWriter, r *http.Request, streaming bool) {
	h.mu.Lock()
	records := append([]*pb.SequencedRecord(nil), h.log...)
	fault := readFault{After: -1}
	if streaming {
		if h.reads < len(h.script.ReadFaults) {
			fault = h.script.ReadFaults[h.reads]
		}
		h.reads++
		h.history = append(h.history, "read "+r.URL.RawQuery)
	}
	h.mu.Unlock()
	query := r.URL.Query()
	start, _ := strconv.ParseUint(query.Get("seq_num"), 10, 64)
	if start > uint64(len(records)) {
		h.replyError(w, streaming, http.StatusRequestedRangeNotSatisfiable, "range_not_satisfiable")
		return
	}
	tail := &pb.StreamPosition{SeqNum: uint64(len(records)), Timestamp: 42}
	records = records[start:]
	if raw := query.Get("count"); raw != "" {
		count, _ := strconv.ParseUint(raw, 10, 64)
		if count < uint64(len(records)) {
			records = records[:count]
		}
	}
	if raw := query.Get("bytes"); raw != "" {
		limit, _ := strconv.ParseUint(raw, 10, 64)
		var used uint64
		for i, record := range records {
			used += uint64(8 + len(record.Body) + 2*len(record.Headers))
			for _, header := range record.Headers {
				used += uint64(len(header.Name) + len(header.Value))
			}
			if used > limit {
				records = records[:i]
				break
			}
		}
	}
	if !streaming {
		h.replyProto(w, &pb.ReadBatch{Records: records, Tail: tail}, false)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	for i := 0; i <= len(records); i++ {
		if i == fault.After {
			switch fault.Kind {
			case "reset":
				panic(http.ErrAbortHandler)
			case "unavailable":
				h.replyError(w, true, http.StatusServiceUnavailable, "unavailable")
			case "truncate":
				_, _ = w.Write([]byte{0, 0, 10, 0, 1})
			}
			return
		}
		if i == len(records) {
			return
		}
		if !h.replyProto(w, &pb.ReadBatch{Records: records[i : i+1]}, true) {
			return
		}
		// A read fault fires after delivery, independently of HTTP/2 buffering.
		if h.delivered != nil {
			select {
			case <-h.delivered:
			case <-r.Context().Done():
				return
			}
		}
	}
}

func (h *harness) replyError(w http.ResponseWriter, streaming bool, status int, code string) {
	data, _ := json.Marshal(s2.ErrorInfo{Code: code, Message: code})
	if !streaming {
		w.WriteHeader(status)
		_, _ = w.Write(data)
		return
	}
	body := make([]byte, 2, 2+len(data))
	binary.BigEndian.PutUint16(body, uint16(status))
	h.writeFrame(w, append(body, data...), 0x80)
}

func (h *harness) replyProto(w http.ResponseWriter, message proto.Message, streaming bool) bool {
	h.mu.Lock()
	corrupt := h.corrupt
	h.mu.Unlock()
	if corrupt != nil {
		corrupt(message)
	}
	data, err := proto.Marshal(message)
	if err != nil {
		return false
	}
	if !streaming {
		_, err = w.Write(data)
		return err == nil
	}
	compression := h.script.Compression
	if len(data) < 1024 {
		compression = s2.CompressionNone
	}
	data, err = framing.Compress(data, compression)
	return err == nil && h.writeFrame(w, data, byte(compression)<<5)
}

func (h *harness) writeFrame(w http.ResponseWriter, data []byte, flags byte) bool {
	n := len(data) + 1
	frame := append([]byte{byte(n >> 16), byte(n >> 8), byte(n), flags}, data...)
	for len(frame) > 0 {
		n = len(frame)
		if h.script.Fragment > 0 && n > h.script.Fragment {
			n = h.script.Fragment
		}
		if _, err := w.Write(frame[:n]); err != nil {
			return false
		}
		w.(http.Flusher).Flush()
		frame = frame[n:]
	}
	return true
}
