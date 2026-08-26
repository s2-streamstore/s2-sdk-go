package s2

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// Sessions allowed to share one connection before another is opened.
//
// HTTP/2 would happily carry every session of a client on a single
// connection, which pins them all to one server: a rollout or a failure there
// disrupts the client's entire workload at once, and they all reconnect in the
// same instant. Spreading them bounds that blast radius.
const maxSessionsPerConnection = 4

// spreadTransport hands streaming requests to one of several transports per
// host, each of which keeps its own connections, so long-lived sessions do not
// all pile onto the same one.
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

// checkout returns the host's first transport with room, adding one if they
// are all at capacity.
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

// poison drops the entry from the host's pool so no new session reuses its
// connections, which are pinned to a draining server. Entries for servers that
// are not going away stay pooled, sessions already on the entry keep their
// connections, and poisoning the same entry again is a no-op.
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

// poisonHandle grants its holder the ability to poison the pooled entry a
// streaming response was served on.
type poisonHandle struct {
	pool  *spreadTransport
	host  string
	entry *spreadEntry
}

type poisonCaptureKey struct{}

// capturePoisonHandle returns a context that captures, for the streaming
// request made with it, the handle for poisoning the pooled entry the response
// is served on.
func capturePoisonHandle(ctx context.Context) (context.Context, *poisonCapture) {
	capture := &poisonCapture{}
	return context.WithValue(ctx, poisonCaptureKey{}, capture), capture
}

// poisonCapture receives a [poisonHandle] once the transport serves the
// request it was captured with.
type poisonCapture struct {
	handle atomic.Pointer[poisonHandle]
}

// poison drops the captured pooled entry, if one was captured, so the next
// session dials afresh instead of reusing a connection pinned to a draining
// server.
func (c *poisonCapture) poison() {
	if c == nil {
		return
	}
	if h := c.handle.Load(); h != nil {
		h.pool.poison(h.host, h.entry)
	}
}

// releaseOnClose frees the transport slot once the caller is done reading.
type releaseOnClose struct {
	io.ReadCloser
	entry *spreadEntry
	once  sync.Once
}

func (r *releaseOnClose) Close() error {
	r.once.Do(func() { r.entry.sessions.Add(-1) })
	return r.ReadCloser.Close()
}
