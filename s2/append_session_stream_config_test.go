package s2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// appendSessionStreamConfig returns a fully-populated StreamConfig exercising
// every nested pointer, mirroring the coverage of TestStreamConfigSnapshotAcrossRetries.
func appendSessionStreamConfig() *StreamConfig {
	return &StreamConfig{
		DeleteOnEmpty:   &DeleteOnEmptyConfig{MinAgeSecs: Int64(300)},
		RetentionPolicy: &RetentionPolicy{Age: Int64(3600)},
		StorageClass:    Ptr("express"),
		Timestamping: &TimestampingConfig{
			Mode:     Ptr(TimestampingModeClientPrefer),
			Uncapped: Bool(false),
		},
	}
}

const appendSessionStreamConfigHeader = `{"delete_on_empty":{"min_age_secs":300},"retention_policy":{"age":3600},"storage_class":"express","timestamping":{"mode":"client-prefer","uncapped":false}}`

// retryable503Body is a server error body that decodes to a retryable S2Error
// (code "unavailable", HTTP 503), which the append session treats as retryable
// without the server-draining special case.
func retryable503Body() []byte {
	return []byte(`{"code":"unavailable","message":"test failure"}`)
}

func appendSession503Response(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(bytes.NewReader(retryable503Body())),
		Header:     make(http.Header),
		Request:    req,
	}
}

// TestApplyAppendSessionDefaultsSnapshotsStreamConfig pins the fix directly:
// the session must retain a deep clone of the caller's StreamConfig rather
// than the caller's pointer, so subsequent caller mutation is invisible to
// the session.
func TestApplyAppendSessionDefaultsSnapshotsStreamConfig(t *testing.T) {
	config := appendSessionStreamConfig()
	opts := &AppendSessionOptions{StreamConfig: config}

	applied, err := applyAppendSessionDefaults(opts, DefaultRetryConfig)
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}

	if applied.StreamConfig == nil {
		t.Fatal("expected non-nil snapshot StreamConfig")
	}
	if applied.StreamConfig == config {
		t.Fatal("expected StreamConfig to be a distinct pointer (snapshot), not the caller's struct")
	}

	// Mutate every nested pointer of the caller's config and reassign the
	// pointer fields; the snapshot must keep the originally-supplied values.
	*config.RetentionPolicy.Age = 9999
	*config.DeleteOnEmpty.MinAgeSecs = 9999
	*config.StorageClass = "standard"
	*config.Timestamping.Mode = TimestampingModeArrival
	*config.Timestamping.Uncapped = true
	config.RetentionPolicy = nil
	config.DeleteOnEmpty = nil
	config.StorageClass = nil
	config.Timestamping = nil

	if got := *applied.StreamConfig.RetentionPolicy.Age; got != 3600 {
		t.Errorf("snapshot RetentionPolicy.Age = %d, want 3600", got)
	}
	if got := *applied.StreamConfig.DeleteOnEmpty.MinAgeSecs; got != 300 {
		t.Errorf("snapshot DeleteOnEmpty.MinAgeSecs = %d, want 300", got)
	}
	if got := *applied.StreamConfig.StorageClass; got != "express" {
		t.Errorf("snapshot StorageClass = %q, want %q", got, "express")
	}
	if got := *applied.StreamConfig.Timestamping.Mode; got != TimestampingModeClientPrefer {
		t.Errorf("snapshot Timestamping.Mode = %q, want %q", got, TimestampingModeClientPrefer)
	}
	if got := *applied.StreamConfig.Timestamping.Uncapped; got {
		t.Errorf("snapshot Timestamping.Uncapped = %v, want false", got)
	}
}

// TestApplyAppendSessionDefaultsPreservesNilStreamConfig ensures a nil config
// stays nil (and no spurious empty header is sent) after applying defaults.
func TestApplyAppendSessionDefaultsPreservesNilStreamConfig(t *testing.T) {
	opts := &AppendSessionOptions{}
	applied, err := applyAppendSessionDefaults(opts, DefaultRetryConfig)
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if applied.StreamConfig != nil {
		t.Errorf("expected nil StreamConfig preserved, got %+v", applied.StreamConfig)
	}
}

// TestApplyAppendSessionDefaultsSnapshotsEmptyStreamConfig ensures an
// explicitly-empty (but non-nil) config is cloned to a distinct pointer so the
// "{}" header is stable and immune to later caller mutation.
func TestApplyAppendSessionDefaultsSnapshotsEmptyStreamConfig(t *testing.T) {
	config := &StreamConfig{}
	opts := &AppendSessionOptions{StreamConfig: config}

	applied, err := applyAppendSessionDefaults(opts, DefaultRetryConfig)
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if applied.StreamConfig == nil {
		t.Fatal("expected non-nil snapshot for empty config")
	}
	if applied.StreamConfig == config {
		t.Fatal("expected distinct pointer for empty config snapshot")
	}

	config.StorageClass = Ptr("standard")
	if applied.StreamConfig.StorageClass != nil {
		t.Errorf("snapshot should remain empty, got StorageClass %v", *applied.StreamConfig.StorageClass)
	}
}

// TestAppendSessionStreamConfigSnapshotAcrossReconnect is the direct analog of
// TestStreamConfigSnapshotAcrossRetries for the AppendSession path. Every
// transport (re)connect re-sends the s2-stream-config header; the header must
// reflect the snapshot captured when the session was created, not the caller's
// live struct. The round tripper forces a reconnect by returning a retryable 503
// on every attempt and mutates the caller's config after the first connect.
func TestAppendSessionStreamConfigSnapshotAcrossReconnect(t *testing.T) {
	config := appendSessionStreamConfig()

	var mu sync.Mutex
	var headers []string
	var connects int
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		headers = append(headers, req.Header.Get(s2StreamConfigHeader))
		connects++
		if connects == 1 {
			// Blow away the caller's config after the initial connect encoded
			// the original snapshot. Without the fix, the reconnect would
			// re-marshal this mutated struct.
			*config = StreamConfig{}
		}
		mu.Unlock()
		return appendSession503Response(req), nil
	})

	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{
		RetryConfig:  retryCfg,
		StreamConfig: config,
	})
	if err != nil {
		t.Fatalf("append session: %v", err)
	}

	future, err := session.Submit(&AppendInput{
		Records: []AppendRecord{{Body: []byte("hello")}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if _, err := ticket.Ack(ctx); err == nil {
		t.Fatal("expected retries to be exhausted, got success")
	} else if !errors.Is(err, ErrMaxAttemptsExhausted) {
		t.Fatalf("expected ErrMaxAttemptsExhausted, got %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	captured := headers
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 connects (initial + reconnect), got %d: %v", len(captured), captured)
	}
	for i, h := range captured {
		if h != appendSessionStreamConfigHeader {
			t.Errorf("connect %d: expected stream config header %q, got %q", i+1, appendSessionStreamConfigHeader, h)
		}
	}
}

// TestAppendSessionStreamConfigNoMutationRace drives several reconnects while a
// concurrent goroutine continuously mutates the caller's StreamConfig. With the
// fix, the pump marshals a snapshot and never touches the caller's struct, so
// there is no data race and every reconnect header equals the original
// snapshot. Run with -race to confirm the race is eliminated; without the fix,
// the race detector reports a read in encoding/json.Marshal racing the caller's
// writes, and the captured headers diverge.
func TestAppendSessionStreamConfigNoMutationRace(t *testing.T) {
	config := appendSessionStreamConfig()

	const reconnects = 12
	var mu sync.Mutex
	var headers []string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		headers = append(headers, req.Header.Get(s2StreamConfigHeader))
		mu.Unlock()
		return appendSession503Response(req), nil
	})

	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       reconnects,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{
		RetryConfig:  retryCfg,
		StreamConfig: config,
	})
	if err != nil {
		t.Fatalf("append session: %v", err)
	}

	stop := make(chan struct{})
	mutatorDone := make(chan struct{})
	go func() {
		defer close(mutatorDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			config.RetentionPolicy = &RetentionPolicy{Age: Int64(time.Now().UnixNano())}
			config.DeleteOnEmpty = &DeleteOnEmptyConfig{MinAgeSecs: Int64(time.Now().UnixNano() % 1000)}
			config.StorageClass = Ptr("standard")
			config.Timestamping = &TimestampingConfig{
				Mode:     Ptr(TimestampingModeArrival),
				Uncapped: Bool(true),
			}
		}
	}()

	stopMutator := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		<-mutatorDone
	}

	future, err := session.Submit(&AppendInput{
		Records: []AppendRecord{{Body: []byte("hello")}},
	})
	if err != nil {
		stopMutator()
		t.Fatalf("submit: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		stopMutator()
		t.Fatalf("wait: %v", err)
	}
	if _, err := ticket.Ack(ctx); err == nil {
		stopMutator()
		t.Fatal("expected retries to be exhausted, got success")
	} else if !errors.Is(err, ErrMaxAttemptsExhausted) {
		stopMutator()
		t.Fatalf("expected ErrMaxAttemptsExhausted, got %v", err)
	}

	stopMutator()

	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	captured := headers
	mu.Unlock()
	if len(captured) != reconnects {
		t.Fatalf("expected %d connects, got %d: %v", reconnects, len(captured), captured)
	}
	for i, h := range captured {
		if h != appendSessionStreamConfigHeader {
			t.Errorf("connect %d: expected stream config header %q, got %q", i+1, appendSessionStreamConfigHeader, h)
		}
	}
}
