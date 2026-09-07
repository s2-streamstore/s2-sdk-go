package faulttest

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestLiteDurability(t *testing.T) {
	checker := checkerBinary(t)
	if script := loadConcurrentTrace(t); script != nil {
		checkLiteDurability(t, checker, *script)
		return
	}
	for writer, kind := range []string{opAppend, opSession, opProducer} {
		t.Run(kind, func(t *testing.T) {
			script := concurrentScenario(byte(writer))
			for i := range 3 {
				script.Waves[4][i].Fault = ""
			}
			script.Waves[4][writer].Fault = crashAfterCommit
			script.Waves[4][(writer+1)%3].Fault = beforeCommit
			checkLiteDurability(t, checker, script)
		})
	}
}

func checkLiteDurability(t *testing.T, checker string, script concurrentTrace) {
	t.Helper()
	lite := newLite(t)
	basin, name := provision(t, lite.endpoint)
	var once sync.Once
	var crashErr error
	runConcurrent(t, checker, lite.endpoint, basin, name, script, func() error {
		once.Do(func() { crashErr = lite.kill() })
		return crashErr
	}, lite.start)
	if lite.generation != 2 {
		t.Fatalf("expected an injected crash and restart, got %d generations", lite.generation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	stream := testClient(lite.endpoint, s2.CompressionNone).Basin(basin).Stream(s2.StreamName(name))
	before, err := readPrefix(ctx, stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lite.kill(); err != nil {
		t.Fatal(err)
	}
	if err := lite.start(); err != nil {
		t.Fatal(err)
	}
	after, err := readPrefix(ctx, stream, nil)
	if err != nil || !sameRecords(before, after) {
		t.Fatalf("durable records changed after SIGKILL: before=%d after=%d err=%v", len(before), len(after), err)
	}
	input := &s2.AppendInput{Records: []s2.AppendRecord{{Body: []byte("after-restart")}}, MatchSeqNum: s2.Uint64(uint64(len(after))), FencingToken: s2.String("stale-after-restart")}
	_, err = stream.Append(ctx, input)
	var apiErr *s2.S2Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale fence accepted after restart: %v", err)
	}
	for _, record := range after {
		if record.IsCommandRecord() && string(record.Headers[0].Value) == fenceCommand {
			input.FencingToken = s2.String(string(record.Body))
		}
	}
	ack, err := stream.Append(ctx, input)
	if err != nil || ack.Start.SeqNum != uint64(len(after)) {
		t.Fatalf("append at recovered tail: ack=%+v err=%v", ack, err)
	}
}

func TestConcurrentEndpoint(t *testing.T) {
	endpoint := os.Getenv("S2_FAULT_ENDPOINT")
	if endpoint == "" {
		t.Skip("set S2_FAULT_ENDPOINT to test an existing shared S2 endpoint")
	}
	checker := checkerBinary(t)
	basin, name := provision(t, endpoint)
	runConcurrent(t, checker, endpoint, basin, name, concurrentScenario(0), nil, nil)
}
