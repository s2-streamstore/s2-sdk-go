package s2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"google.golang.org/protobuf/proto"
)

const testStreamConfigHeader = `{"delete_on_empty":{"min_age_secs":300},"retention_policy":{"age":3600}}`

func testStreamConfig() *StreamConfig {
	return &StreamConfig{
		RetentionPolicy: &RetentionPolicy{Age: Int64(3600)},
		DeleteOnEmpty:   &DeleteOnEmptyConfig{MinAgeSecs: Int64(300)},
	}
}

func TestAppendSetsStreamConfigHeader(t *testing.T) {
	rt := &streamConfigHeaderRoundTripper{response: &pb.AppendAck{}}
	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := stream.Append(ctx, &AppendInput{
		Records:      []AppendRecord{{Body: []byte("hello")}},
		StreamConfig: testStreamConfig(),
	})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	if rt.header != testStreamConfigHeader {
		t.Fatalf("expected stream config header %q, got %q", testStreamConfigHeader, rt.header)
	}
}

func TestReadSetsStreamConfigHeader(t *testing.T) {
	rt := &streamConfigHeaderRoundTripper{response: &pb.ReadBatch{}}
	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := stream.Read(ctx, &ReadOptions{Count: Uint64(1), StreamConfig: testStreamConfig()})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if rt.header != testStreamConfigHeader {
		t.Fatalf("expected stream config header %q, got %q", testStreamConfigHeader, rt.header)
	}
}

func TestAppendSessionSetsStreamConfigHeader(t *testing.T) {
	rt := &streamConfigHeaderRoundTripper{headerCh: make(chan string, 1)}
	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	session, err := stream.createAppendSession(ctx, testStreamConfig())
	if err != nil {
		t.Fatalf("create append session failed: %v", err)
	}
	defer session.Close()

	assertHeaderCaptured(t, rt.headerCh, testStreamConfigHeader)
}

func TestReadSessionSetsStreamConfigHeader(t *testing.T) {
	rt := &streamConfigHeaderRoundTripper{headerCh: make(chan string, 1)}
	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	reader := &streamReader{
		streamClient: stream,
		logger:       stream.logger,
	}

	opts := &ReadOptions{Count: Uint64(1), StreamConfig: testStreamConfig()}
	if err := reader.runOnce(ctx, opts); err != nil {
		t.Fatalf("read session runOnce failed: %v", err)
	}

	assertHeaderCaptured(t, rt.headerCh, testStreamConfigHeader)
}

func TestReadSessionStreamConfigAcrossAttempts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   *ReadOptions
		header string
	}{
		{name: "config only", opts: &ReadOptions{StreamConfig: testStreamConfig()}, header: testStreamConfigHeader},
		{name: "empty config", opts: &ReadOptions{StreamConfig: &StreamConfig{}}, header: "{}"},
		{name: "empty options", opts: &ReadOptions{}},
		{name: "nil options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan *http.Request, 2)
			attempts := 0
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests <- req
				attempts++
				status := http.StatusOK
				if attempts == 1 {
					status = http.StatusInternalServerError
				}
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})
			stream := newFrameServedStreamClient(rt)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			session, err := stream.ReadSession(ctx, tc.opts)
			if err != nil {
				t.Fatalf("read session: %v", err)
			}
			defer session.Close()
			if session.Next() {
				t.Fatal("unexpected record")
			}
			if err := session.Err(); err != nil {
				t.Fatalf("read session error: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("read session timed out")
			}
			if len(requests) != 2 {
				t.Fatalf("expected two requests, got %d", len(requests))
			}
			for attempt := 1; attempt <= 2; attempt++ {
				req := <-requests
				if got := req.Header.Get(s2StreamConfigHeader); got != tc.header {
					t.Errorf("attempt %d: expected stream config header %q, got %q", attempt, tc.header, got)
				}
				_, present := req.Header[http.CanonicalHeaderKey(s2StreamConfigHeader)]
				if present != (tc.header != "") {
					t.Errorf("attempt %d: unexpected stream config header presence: %t", attempt, present)
				}
				if req.URL.RawQuery != "" {
					t.Errorf("attempt %d: unexpected query parameters %q", attempt, req.URL.RawQuery)
				}
			}
		})
	}
}

func TestStreamConfigSnapshotAcrossRetries(t *testing.T) {
	for _, operation := range []string{"append", "read session"} {
		t.Run(operation, func(t *testing.T) {
			config := &StreamConfig{
				DeleteOnEmpty:   &DeleteOnEmptyConfig{MinAgeSecs: Int64(300)},
				RetentionPolicy: &RetentionPolicy{Age: Int64(3600)},
				StorageClass:    Ptr(StorageClassExpress),
				Timestamping: &TimestampingConfig{
					Mode:     Ptr(TimestampingModeClientPrefer),
					Uncapped: Bool(false),
				},
			}
			const expectedHeader = `{"delete_on_empty":{"min_age_secs":300},"retention_policy":{"age":3600},"storage_class":"express","timestamping":{"mode":"client-prefer","uncapped":false}}`
			headers := make(chan string, 2)
			attempts := 0
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				headers <- req.Header.Get(s2StreamConfigHeader)
				attempts++
				status := http.StatusOK
				if attempts == 1 {
					*config.DeleteOnEmpty.MinAgeSecs = 600
					*config.RetentionPolicy.Age = 7200
					*config.StorageClass = StorageClassStandard
					*config.Timestamping.Mode = TimestampingModeArrival
					*config.Timestamping.Uncapped = true
					config.DeleteOnEmpty.MinAgeSecs = nil
					config.RetentionPolicy.Age = nil
					config.Timestamping.Mode = nil
					config.Timestamping.Uncapped = nil
					*config = StreamConfig{}
					status = http.StatusInternalServerError
				}
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})
			stream := newTestStreamClientWithTransport(rt)
			stream.basinClient.retryConfig = &RetryConfig{
				MaxAttempts:       2,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: AppendRetryPolicyAll,
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			switch operation {
			case "append":
				if _, err := stream.Append(ctx, &AppendInput{
					Records:      []AppendRecord{{Body: []byte("hello")}},
					StreamConfig: config,
				}); err != nil {
					t.Fatalf("append failed: %v", err)
				}
			case "read session":
				session, err := stream.ReadSession(ctx, &ReadOptions{StreamConfig: config})
				if err != nil {
					t.Fatalf("read session: %v", err)
				}
				defer session.Close()
				if session.Next() {
					t.Fatal("unexpected record")
				}
				if err := session.Err(); err != nil {
					t.Fatalf("read session error: %v", err)
				}
			}
			if ctx.Err() != nil {
				t.Fatal("operation timed out")
			}
			if len(headers) != 2 {
				t.Fatalf("expected two requests, got %d", len(headers))
			}
			for attempt := 1; attempt <= 2; attempt++ {
				if got := <-headers; got != expectedHeader {
					t.Errorf("attempt %d: expected stream config header %q, got %q", attempt, expectedHeader, got)
				}
			}
		})
	}
}

func TestAppendWithoutStreamConfigOmitsHeader(t *testing.T) {
	rt := &streamConfigHeaderRoundTripper{response: &pb.AppendAck{}}
	stream := newTestStreamClientWithTransport(rt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := stream.Append(ctx, &AppendInput{
		Records: []AppendRecord{{Body: []byte("hello")}},
	})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	if rt.present {
		t.Fatalf("expected no stream config header, got %q", rt.header)
	}
}

type streamConfigHeaderRoundTripper struct {
	header   string
	present  bool
	headerCh chan string
	response proto.Message
}

func (r *streamConfigHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.header = req.Header.Get(s2StreamConfigHeader)
	_, r.present = req.Header[http.CanonicalHeaderKey(s2StreamConfigHeader)]
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

func newTestStreamClientWithTransport(rt http.RoundTripper) *StreamClient {
	httpClient := &http.Client{Transport: rt}
	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		httpClient:  httpClient,
	}
	basin.client = &Client{streamingClient: httpClient}

	return basin.Stream("test")
}
