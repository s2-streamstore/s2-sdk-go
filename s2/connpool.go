package s2

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// Sessions allowed to share one connection before another is opened.
const maxSessionsPerConnection = 4

// spreadTransport spreads streaming requests across transports per host.
type spreadTransport struct {
	newTransport func() http.RoundTripper

	mu    sync.Mutex
	hosts map[string][]*spreadEntry
}

type spreadEntry struct {
	rt       http.RoundTripper
	sessions atomic.Int64
}

func newSpreadTransport(newTransport func() http.RoundTripper) *spreadTransport {
	return &spreadTransport{
		newTransport: newTransport,
		hosts:        make(map[string][]*spreadEntry),
	}
}

func (t *spreadTransport) checkout(host string) *spreadEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, entry := range t.hosts[host] {
		if entry.sessions.Load() < maxSessionsPerConnection {
			entry.sessions.Add(1)
			return entry
		}
	}
	entry := &spreadEntry{rt: t.newTransport()}
	entry.sessions.Add(1)
	t.hosts[host] = append(t.hosts[host], entry)
	return entry
}

// poison removes the target from the pool while existing sessions keep it alive.
func (t *spreadTransport) poison(host string, target *spreadEntry) {
	t.mu.Lock()
	entries := t.hosts[host]
	kept := entries[:0]
	removed := false
	for _, entry := range entries {
		if entry == target {
			removed = true
		} else {
			kept = append(kept, entry)
		}
	}
	t.hosts[host] = kept
	t.mu.Unlock()

	if removed {
		closeIdleConnections(target.rt)
	}
}

func (t *spreadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	entry := t.checkout(host)

	if capture, ok := req.Context().Value(poisonCaptureKey{}).(*poisonCapture); ok {
		capture.handle.Store(&poisonHandle{pool: t, host: host, entry: entry})
	}

	resp, err := entry.rt.RoundTrip(req)
	if err != nil {
		entry.sessions.Add(-1)
		return nil, err
	}

	// A streaming response occupies the connection until its body is closed.
	resp.Body = &releaseOnClose{ReadCloser: resp.Body, entry: entry}
	return resp, nil
}

func (t *spreadTransport) CloseIdleConnections() {
	t.mu.Lock()
	entries := make([]*spreadEntry, 0, len(t.hosts)*maxSessionsPerConnection)
	for _, hostEntries := range t.hosts {
		entries = append(entries, hostEntries...)
	}
	t.mu.Unlock()

	for _, entry := range entries {
		closeIdleConnections(entry.rt)
	}
}

func closeIdleConnections(rt http.RoundTripper) {
	if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// poisonHandle identifies the pooled entry that served a streaming response.
type poisonHandle struct {
	pool  *spreadTransport
	host  string
	entry *spreadEntry
}

type poisonCaptureKey struct{}

func capturePoisonHandle(ctx context.Context) (context.Context, *poisonCapture) {
	capture := &poisonCapture{}
	return context.WithValue(ctx, poisonCaptureKey{}, capture), capture
}

type poisonCapture struct {
	handle atomic.Pointer[poisonHandle]
}

func (c *poisonCapture) poison() {
	if c == nil {
		return
	}
	if h := c.handle.Load(); h != nil {
		h.pool.poison(h.host, h.entry)
	}
}

type releaseOnClose struct {
	io.ReadCloser
	entry *spreadEntry
	once  sync.Once
}

func (r *releaseOnClose) Close() error {
	r.once.Do(func() { r.entry.sessions.Add(-1) })
	return r.ReadCloser.Close()
}
