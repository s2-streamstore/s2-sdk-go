package faulttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type harness struct {
	script      trace
	proxy       *faultProxy
	basin, name string
	dir         string
}

func newHarness(t *testing.T, endpoint string, script trace) *harness {
	t.Helper()
	basin, name := provision(t, endpoint)
	plans := make([]faultPlan, len(script.Batches)+1)
	for i, batch := range script.Batches {
		plans[i].Fault = batch.Fault
	}
	p := newFaultProxy(t, endpoint, plans)
	dir := resultDir(t)
	data, _ := json.MarshalIndent(script, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "trace.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		wire, _ := json.MarshalIndent(p.wire, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "wire.json"), wire, 0600)
	})
	return &harness{script: script, proxy: p, basin: basin, name: name, dir: dir}
}

func (h *harness) stream(id int) *s2.StreamClient {
	endpoint := fmt.Sprintf("%s/op/%d/v1", h.proxy.endpoint, id)
	client := s2.New("test", &s2.ClientOptions{
		BaseURL: endpoint, MakeBasinBaseURL: func(string) string { return endpoint },
		RequestTimeout: 3 * time.Second,
		RetryConfig:    &s2.RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: h.script.Policy},
	})
	return client.Basin(h.basin).Stream(s2.StreamName(h.name))
}
