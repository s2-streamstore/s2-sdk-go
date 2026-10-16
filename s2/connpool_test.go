package s2

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type idleCountingRoundTripper struct {
	idleCloses atomic.Int32
}

func (rt *idleCountingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func (rt *idleCountingRoundTripper) CloseIdleConnections() {
	rt.idleCloses.Add(1)
}

func TestSpreadTransportClosesPoisonedEntryWhenLastSessionEnds(t *testing.T) {
	rt := &idleCountingRoundTripper{}
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	req, err := http.NewRequest(http.MethodGet, "https://example.com/records", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, capture := capturePoisonHandle(req.Context())
	first, err := pool.RoundTrip(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	// Poisoning while streams are open cannot close the connection yet.
	capture.poison()
	before := rt.idleCloses.Load()

	first.Body.Close()
	if got := rt.idleCloses.Load(); got != before {
		t.Fatalf("closed idle connections with a session still open (%d -> %d)", before, got)
	}
	second.Body.Close()
	if got := rt.idleCloses.Load(); got != before+1 {
		t.Fatalf("expected idle connections closed once the last session ended, got %d -> %d", before, got)
	}
	second.Body.Close()
	if got := rt.idleCloses.Load(); got != before+1 {
		t.Fatalf("repeated body close released the session again (%d)", got)
	}
}
