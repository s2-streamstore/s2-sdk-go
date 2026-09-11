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
