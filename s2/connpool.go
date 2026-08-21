package s2

import (
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

// spreadTransport hands streaming requests to one of several transports, each
// of which keeps its own connections, so long-lived sessions do not all pile
// onto the same one.
type spreadTransport struct {
	newTransport func() http.RoundTripper

	mu      sync.Mutex
	entries []*spreadEntry
}

type spreadEntry struct {
	rt       http.RoundTripper
	sessions atomic.Int64
}

func newSpreadTransport(newTransport func() http.RoundTripper) *spreadTransport {
	return &spreadTransport{newTransport: newTransport}
}

// checkout returns the first transport with room, adding one if they are all
// at capacity.
func (t *spreadTransport) checkout() *spreadEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, entry := range t.entries {
		if entry.sessions.Load() < maxSessionsPerConnection {
			entry.sessions.Add(1)
			return entry
		}
	}
	entry := &spreadEntry{rt: t.newTransport()}
	entry.sessions.Add(1)
	t.entries = append(t.entries, entry)
	return entry
}

func (t *spreadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := t.checkout()

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
	entries := make([]*spreadEntry, len(t.entries))
	copy(entries, t.entries)
	t.mu.Unlock()

	for _, entry := range entries {
		if closer, ok := entry.rt.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
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
