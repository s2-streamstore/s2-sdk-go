package faulttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type liteProcess struct {
	mu         sync.Mutex
	binary     string
	args       []string
	root       string
	port       string
	endpoint   string
	cmd        *exec.Cmd
	generation int
}

func newLite(t *testing.T) *liteProcess {
	t.Helper()
	binary := os.Getenv("S2_LITE_BIN")
	if binary == "" {
		t.Skip("set S2_LITE_BIN to a server binary (or s2 with S2_LITE_ARGS='[\"lite\"]')")
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Fatal(err)
	}
	lite := &liteProcess{binary: path, root: resultDir(t)}
	if raw := os.Getenv("S2_LITE_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &lite.args); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lite.port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	lite.endpoint = "http://127.0.0.1:" + lite.port
	_ = listener.Close()
	t.Cleanup(func() { _ = lite.kill() })
	if err := lite.start(); err != nil {
		t.Fatal(err)
	}
	return lite
}

func (p *liteProcess) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}
	p.generation++
	logPath := filepath.Join(p.root, fmt.Sprintf("server-%d.log", p.generation))
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	args := append(append([]string(nil), p.args...), "--port", p.port, "--local-root", filepath.Join(p.root, "storage"))
	cmd := exec.Command(p.binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "S2LITE_INIT_FILE=") && !strings.HasPrefix(entry, "RUST_LOG=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "RUST_LOG=error")
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	client := &http.Client{Timeout: 300 * time.Millisecond}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(p.endpoint + "/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			p.cmd = nil
			data, _ := os.ReadFile(logPath)
			return fmt.Errorf("Lite did not become ready: %s", data)
		}
	}
}

func (p *liteProcess) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = p.cmd.Wait()
	p.cmd = nil
	return nil
}

func provision(t *testing.T, endpoint string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testClient(endpoint, s2.CompressionNone)
	basin := s2.BasinName(fmt.Sprintf("sdk-fault-%d", time.Now().UnixNano()))
	if _, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basin}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Basins.Delete(ctx, basin)
	})
	name := s2.StreamName("history")
	if _, err := client.Basin(string(basin)).Streams.Create(ctx, s2.CreateStreamArgs{Stream: name}); err != nil {
		t.Fatal(err)
	}
	return string(basin), string(name)
}

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
