package bentobox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// A large prefix is discovered one page per call, a failed page is retried
// from the same cursor, and the complete set (used for pruning) is only
// produced once every page has been visited.
func TestPrefixedDiscoveryPagesAcrossCalls(t *testing.T) {
	const prefix = "p/"
	const total = 2500
	deleted := fmt.Sprintf("%s%05d", prefix, listStreamsPageLimit-1)

	var names []string
	for i := 0; i < total; i++ {
		names = append(names, fmt.Sprintf("%s%05d", prefix, i))
	}
	names = append(names, "q/other")
	sort.Strings(names)

	var mu sync.Mutex
	var startAfters []string
	failNext := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		startAfters = append(startAfters, q.Get("start_after"))
		fail := failNext
		failNext = false
		mu.Unlock()
		if fail {
			http.Error(w, `{"code":"internal","message":"boom"}`, http.StatusInternalServerError)
			return
		}

		limit, _ := strconv.Atoi(q.Get("limit"))
		var resp s2.ListStreamsResponse
		for _, name := range names {
			if !strings.HasPrefix(name, q.Get("prefix")) || name <= q.Get("start_after") {
				continue
			}
			if len(resp.Streams) == limit {
				resp.HasMore = true
				break
			}
			info := s2.StreamInfo{Name: s2.StreamName(name), CreatedAt: time.Unix(0, 0)}
			if name == deleted {
				info.DeletedAt = &info.CreatedAt
			}
			resp.Streams = append(resp.Streams, info)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	basin := s2.New("token", &s2.ClientOptions{
		BaseURL:          srv.URL,
		MakeBasinBaseURL: func(string) string { return srv.URL },
		RetryConfig:      &s2.RetryConfig{MaxAttempts: 1},
	}).Basin("basin")

	ctx := context.Background()
	d := newStreamDiscovery(PrefixedInputStreams{Prefix: prefix})
	discovered := make(map[string]struct{})
	var complete map[string]struct{}
	calls := 0
	for complete == nil {
		calls++
		if calls > 10 {
			t.Fatal("scan did not complete")
		}
		if calls == 2 {
			mu.Lock()
			failNext = true
			mu.Unlock()
		}

		page, c, err := d.next(ctx, basin)
		if calls == 2 {
			if err == nil {
				t.Fatal("expected injected page failure")
			}
			continue
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		for _, stream := range page {
			discovered[stream] = struct{}{}
		}
		complete = c
	}

	mu.Lock()
	gotStartAfters := append([]string(nil), startAfters...)
	mu.Unlock()
	if len(gotStartAfters) != calls {
		t.Fatalf("expected one list request per call, got %d requests for %d calls", len(gotStartAfters), calls)
	}
	// Page 1 ends on a deleted stream; the cursor must still advance past it,
	// and the failed page 2 must be retried with the same cursor.
	want := []string{"", deleted, deleted, fmt.Sprintf("%s%05d", prefix, 2*listStreamsPageLimit-1)}
	if fmt.Sprint(gotStartAfters) != fmt.Sprint(want) {
		t.Fatalf("start_after sequence = %v, want %v", gotStartAfters, want)
	}

	if len(complete) != total-1 || len(discovered) != total-1 {
		t.Fatalf("expected %d live streams, complete=%d discovered=%d", total-1, len(complete), len(discovered))
	}
	if _, ok := complete[deleted]; ok {
		t.Fatal("deleted stream should not be in the complete set")
	}

	if _, _, err := d.next(ctx, basin); err != nil {
		t.Fatalf("next after complete scan: %v", err)
	}
	mu.Lock()
	restart := startAfters[len(startAfters)-1]
	mu.Unlock()
	if restart != "" {
		t.Fatalf("new scan should start from the beginning, got start_after=%q", restart)
	}
}
