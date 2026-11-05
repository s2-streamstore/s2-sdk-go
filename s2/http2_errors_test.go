package s2

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The streaming client classifies HTTP/2 stream resets and GOAWAY by error
// message because net/http does not export its HTTP/2 error types. Pin that
// those messages are still what Go's HTTP/2 client produces.
func TestStreamingClientClassifiesHTTP2Errors(t *testing.T) {
	var mu sync.Mutex
	var conns []net.Conn
	var server *httptest.Server
	server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		switch r.URL.Path {
		case "/reset":
			panic(http.ErrAbortHandler)
		case "/goaway":
			// Graceful shutdown sends GOAWAY but lets in-flight streams finish, so
			// cut the connection afterwards as a terminating server would.
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_ = server.Config.Shutdown(ctx)
			mu.Lock()
			for _, c := range conns {
				c.Close()
			}
			mu.Unlock()
		}
	}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Config.Protocols = protocols
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}
	server.Start()
	defer server.Close()

	readBody := func(path string) error {
		client := createStreamingClient(5*time.Second, nil, nil)
		resp, err := client.Do(mustRequest(t, server.URL+path))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		return err
	}

	if err := readBody("/reset"); !isStreamResetError(err) || isGoAwayError(err) {
		t.Fatalf("stream reset not classified: %v", err)
	}
	if err := readBody("/goaway"); !isGoAwayError(err) || isStreamResetError(err) {
		t.Fatalf("GOAWAY not classified: %v", err)
	}
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
