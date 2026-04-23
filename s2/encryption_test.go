package s2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"google.golang.org/protobuf/proto"
)

var testEncryptionKeyBytes = []byte{
	1, 2, 3, 4, 5, 6, 7, 8,
	9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24,
	25, 26, 27, 28, 29, 30, 31, 32,
}

const testEncryptionKeyHeaderValue = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="

func TestNewEncryptionKeyTrimsBase64Material(t *testing.T) {
	key, err := NewEncryptionKey("  " + testEncryptionKeyHeaderValue + "\n")
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}

	if got := key.headerValue(); got != testEncryptionKeyHeaderValue {
		t.Fatalf("expected normalized key %q, got %q", testEncryptionKeyHeaderValue, got)
	}
}

func TestNewEncryptionKeyFromBytesEncodesRawMaterial(t *testing.T) {
	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key from bytes failed: %v", err)
	}

	if got := key.headerValue(); got != testEncryptionKeyHeaderValue {
		t.Fatalf("expected encoded key %q, got %q", testEncryptionKeyHeaderValue, got)
	}
}

func TestNewEncryptionKeyFromBytesReportsRawLength(t *testing.T) {
	_, err := NewEncryptionKeyFromBytes(make([]byte, 34))
	if err == nil {
		t.Fatal("expected length validation error")
	}

	var s2Err *S2Error
	if !errors.As(err, &s2Err) {
		t.Fatalf("expected S2Error, got %T", err)
	}
	if s2Err.Origin != "sdk" {
		t.Fatalf("expected sdk-origin error, got %q", s2Err.Origin)
	}
	if !strings.Contains(s2Err.Message, "34") {
		t.Fatalf("expected raw byte length in error message, got %q", s2Err.Message)
	}
}

func TestEncryptionKeyStringRedactsValue(t *testing.T) {
	key, err := NewEncryptionKey(testEncryptionKeyHeaderValue)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}

	if got := key.String(); got != encryptionKeyRedacted {
		t.Fatalf("expected redacted string, got %q", got)
	}

	if got := fmt.Sprintf("%#v", key); got != encryptionKeyRedacted {
		t.Fatalf("expected redacted GoString, got %q", got)
	}
}

func TestBasinConfigStreamCipherJSON(t *testing.T) {
	var config BasinConfig
	if err := json.Unmarshal(
		[]byte(`{"stream_cipher":"aes-256-gcm"}`),
		&config,
	); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if config.StreamCipher == nil || *config.StreamCipher != EncryptionAlgorithmAes256Gcm {
		t.Fatalf("expected stream cipher %q, got %#v", EncryptionAlgorithmAes256Gcm, config.StreamCipher)
	}
}

func TestStreamInfoCipherJSON(t *testing.T) {
	var streamInfo StreamInfo
	if err := json.Unmarshal(
		[]byte(`{"name":"demo","created_at":"2024-01-01T00:00:00Z","cipher":"aegis-256"}`),
		&streamInfo,
	); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if streamInfo.Cipher == nil || *streamInfo.Cipher != EncryptionAlgorithmAegis256 {
		t.Fatalf("expected cipher %q, got %#v", EncryptionAlgorithmAegis256, streamInfo.Cipher)
	}
}

func TestBasinReconfigurationStreamCipherJSON(t *testing.T) {
	algorithm := EncryptionAlgorithmAegis256
	payload, err := json.Marshal(BasinReconfiguration{StreamCipher: &algorithm})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if !bytes.Contains(payload, []byte(`"stream_cipher":"aegis-256"`)) {
		t.Fatalf("expected stream_cipher in payload, got %s", payload)
	}
}

func TestBasinReconfigurationStreamCipherClearJSON(t *testing.T) {
	payload, err := json.Marshal(BasinReconfiguration{ClearStreamCipher: true})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if !bytes.Contains(payload, []byte(`"stream_cipher":null`)) {
		t.Fatalf("expected stream_cipher null in payload, got %s", payload)
	}
}

func TestBasinReconfigurationStreamCipherAbsentJSON(t *testing.T) {
	payload, err := json.Marshal(BasinReconfiguration{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if bytes.Contains(payload, []byte(`"stream_cipher"`)) {
		t.Fatalf("expected stream_cipher absent from payload, got %s", payload)
	}
}

func TestStreamReconfigurationClearFieldsJSON(t *testing.T) {
	cases := []struct {
		name    string
		input   StreamReconfiguration
		wantKey string
	}{
		{"clear storage_class", StreamReconfiguration{ClearStorageClass: true}, `"storage_class":null`},
		{"clear retention_policy", StreamReconfiguration{ClearRetentionPolicy: true}, `"retention_policy":null`},
		{"clear timestamping", StreamReconfiguration{ClearTimestamping: true}, `"timestamping":null`},
		{"clear delete_on_empty", StreamReconfiguration{ClearDeleteOnEmpty: true}, `"delete_on_empty":null`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			if !bytes.Contains(payload, []byte(tc.wantKey)) {
				t.Fatalf("expected %s in payload, got %s", tc.wantKey, payload)
			}
		})
	}
}

func TestStreamReconfigurationAbsentFieldsJSON(t *testing.T) {
	payload, err := json.Marshal(StreamReconfiguration{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, key := range []string{"storage_class", "retention_policy", "timestamping", "delete_on_empty"} {
		if bytes.Contains(payload, []byte(`"`+key+`"`)) {
			t.Fatalf("expected %s absent from payload, got %s", key, payload)
		}
	}
}

func TestTimestampingReconfigurationClearFieldsJSON(t *testing.T) {
	payload, err := json.Marshal(TimestampingReconfiguration{ClearMode: true, ClearUncapped: true})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, want := range []string{`"mode":null`, `"uncapped":null`} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Fatalf("expected %s in payload, got %s", want, payload)
		}
	}
}

func TestDeleteOnEmptyReconfigurationClearJSON(t *testing.T) {
	payload, err := json.Marshal(DeleteOnEmptyReconfiguration{ClearMinAgeSecs: true})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !bytes.Contains(payload, []byte(`"min_age_secs":null`)) {
		t.Fatalf("expected min_age_secs null in payload, got %s", payload)
	}
}

func TestStreamWithOptionsAppendSetsEncryptionHeader(t *testing.T) {
	rt := &encryptionHeaderRoundTripper{response: &pb.AppendAck{}}
	stream, key := newTestEncryptedStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := stream.Append(ctx, &AppendInput{
		Records: []AppendRecord{{Body: []byte("hello")}},
	})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	if rt.header != key.headerValue() {
		t.Fatalf("expected encryption header %q, got %q", key.headerValue(), rt.header)
	}
}

func TestStreamWithOptionsReadSetsEncryptionHeader(t *testing.T) {
	rt := &encryptionHeaderRoundTripper{response: &pb.ReadBatch{}}
	stream, key := newTestEncryptedStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := stream.Read(ctx, &ReadOptions{Count: Uint64(1)})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if rt.header != key.headerValue() {
		t.Fatalf("expected encryption header %q, got %q", key.headerValue(), rt.header)
	}
}

func TestStreamWithOptionsAppendSessionSetsEncryptionHeader(t *testing.T) {
	rt := &encryptionHeaderRoundTripper{headerCh: make(chan string, 1)}
	stream, key := newTestEncryptedStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	session, err := stream.createAppendSession(ctx)
	if err != nil {
		t.Fatalf("create append session failed: %v", err)
	}
	defer session.Close()

	assertHeaderCaptured(t, rt.headerCh, key.headerValue())
}

func TestStreamWithOptionsReadSessionSetsEncryptionHeader(t *testing.T) {
	rt := &encryptionHeaderRoundTripper{headerCh: make(chan string, 1)}
	stream, key := newTestEncryptedStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	reader := &streamReader{
		streamClient: stream,
		closed:       make(chan struct{}),
		logger:       stream.logger,
	}

	if err := reader.runOnce(ctx, &ReadOptions{Count: Uint64(1)}); err != nil {
		t.Fatalf("read session runOnce failed: %v", err)
	}

	assertHeaderCaptured(t, rt.headerCh, key.headerValue())
}

type encryptionHeaderRoundTripper struct {
	header   string
	headerCh chan string
	response proto.Message
}

func (r *encryptionHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.header = req.Header.Get(s2EncryptionKeyHeader)
	if r.headerCh != nil {
		select {
		case r.headerCh <- r.header:
		default:
		}
	}

	body := io.NopCloser(bytes.NewReader(nil))
	if r.response != nil {
		data, err := proto.Marshal(r.response)
		if err != nil {
			return nil, err
		}
		body = io.NopCloser(bytes.NewReader(data))
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func newTestEncryptedStreamClient(t *testing.T, rt http.RoundTripper) (*StreamClient, EncryptionKey) {
	t.Helper()

	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}

	httpClient := &http.Client{Transport: rt}
	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		httpClient:  httpClient,
	}
	basin.client = &Client{streamingClient: httpClient}

	return basin.StreamWithOptions("test", &StreamOptions{EncryptionKey: &key}), key
}

func assertHeaderCaptured(t *testing.T, headerCh <-chan string, want string) {
	t.Helper()

	select {
	case got := <-headerCh:
		if got != want {
			t.Fatalf("expected encryption header %q, got %q", want, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request header")
	}
}
