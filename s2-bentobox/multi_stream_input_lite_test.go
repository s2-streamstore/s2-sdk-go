package bentobox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// Runs against S2 (Lite in CI). Streams span several discovery pages, one list
// call fails, and a stream is deleted mid-run: every stream must be read, the
// failed page must be retried from the same cursor, and only the deleted
// stream's reader may ever be stopped.
func TestStreamsManagerPagedDiscovery(t *testing.T) {
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set, skipping integration test")
	}

	prevLimit := listStreamsPageLimit
	listStreamsPageLimit = 2
	t.Cleanup(func() { listStreamsPageLimit = prevLimit })

	lists := &listRecorder{failAt: 2}
	opts := s2.LoadConfigFromEnv()
	opts.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: lists}
	opts.RetryConfig = &s2.RetryConfig{MaxAttempts: 1}
	client := s2.New(token, opts)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	basinName := s2.BasinName(fmt.Sprintf("bentobox-paged-%d", time.Now().UnixNano()))
	if _, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName}); err != nil {
		t.Fatalf("create basin: %v", err)
	}
	t.Cleanup(func() { _ = client.Basins.Delete(context.Background(), basinName) })
	basin := client.Basin(string(basinName))

	const numStreams = 5
	for i := range numStreams {
		name := s2.StreamName(fmt.Sprintf("paged-%d", i))
		if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: name}); err != nil {
			t.Fatalf("create stream %s: %v", name, err)
		}
		input := &s2.AppendInput{Records: []s2.AppendRecord{{Body: []byte(name)}}}
		if _, err := basin.Stream(name).Append(ctx, input); err != nil {
			t.Fatalf("append to %s: %v", name, err)
		}
	}

	stops := &stopRecorder{}
	inputStream := make(chan recvOutput, numStreams)
	closer := make(chan struct{})
	managerCtx, stopManager := context.WithCancel(ctx)
	go streamsManager(managerCtx, basin, &InputConfig{
		Streams:                 PrefixedInputStreams{Prefix: "paged-"},
		MaxInFlight:             numStreams,
		UpdateStreamsInterval:   50 * time.Millisecond,
		ListStreamsPageInterval: 50 * time.Millisecond,
		Logger:                  recordingLogger{stops: stops},
		BackoffDuration:         50 * time.Millisecond,
		StartSeqNum:             InputStartSeqNumEarliest,
	}, inputStream, closer)
	defer func() {
		stopManager()
		<-closer
	}()

	// Keeps draining so readers never block, until done returns true.
	drainUntil := func(what string, done func(recvOutput) bool) {
		t.Helper()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case out := <-inputStream:
				if out.AckFunc != nil {
					_ = out.AckFunc(ctx, nil)
				}
				if done(out) {
					return
				}
			case <-ticker.C:
				if done(recvOutput{}) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	read := make(map[string]struct{})
	drainUntil("reads from every stream", func(out recvOutput) bool {
		if out.Err == nil && len(out.Batch) > 0 {
			read[out.Stream] = struct{}{}
		}
		return len(read) == numStreams
	})

	starts := lists.startAfters()
	if len(starts) < 3 || starts[1] != starts[2] {
		t.Fatalf("failed list page should be retried with the same cursor; start_after sequence: %q", starts)
	}

	const deleted = "paged-1"
	if err := basin.Streams.Delete(ctx, deleted); err != nil {
		t.Fatalf("delete stream: %v", err)
	}
	drainUntil("deleted stream's reader to stop", func(recvOutput) bool {
		return len(stops.get()) > 0
	})

	// Let several more full scans run; pruning on a partial scan would stop
	// readers for live streams on pages not yet visited.
	scans := lists.scans()
	drainUntil("more discovery scans", func(recvOutput) bool {
		return lists.scans() >= scans+3
	})

	if got := stops.get(); len(got) != 1 || got[0] != deleted {
		t.Fatalf("only %s's reader should stop, got stops for %q", deleted, got)
	}
}

// Records ListStreams requests and fails the failAt-th one.
type listRecorder struct {
	mu     sync.Mutex
	failAt int
	starts []string
}

func (r *listRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/streams") {
		r.mu.Lock()
		r.starts = append(r.starts, req.URL.Query().Get("start_after"))
		n := len(r.starts)
		r.mu.Unlock()
		if n == r.failAt {
			return nil, errors.New("injected list failure")
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (r *listRecorder) startAfters() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.starts...)
}

// Number of scans started, i.e. list requests without a cursor.
func (r *listRecorder) scans() int {
	n := 0
	for _, start := range r.startAfters() {
		if start == "" {
			n++
		}
	}
	return n
}

type stopRecorder struct {
	mu      sync.Mutex
	streams []string
}

func (s *stopRecorder) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.streams...)
}

// Records streams whose reader the manager stops.
type recordingLogger struct {
	silentLogger
	stops  *stopRecorder
	stream string
}

func (l recordingLogger) With(keyValuePairs ...any) Logger {
	for i := 0; i+1 < len(keyValuePairs); i += 2 {
		if keyValuePairs[i] == "stream" {
			l.stream, _ = keyValuePairs[i+1].(string)
		}
	}
	return l
}

func (l recordingLogger) Warn(message string) {
	if message == "Not reading from S2 source anymore" {
		l.stops.mu.Lock()
		l.stops.streams = append(l.stops.streams, l.stream)
		l.stops.mu.Unlock()
	}
}
