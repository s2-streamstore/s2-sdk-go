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

func newLite(t testing.TB) *liteProcess {
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
	name := s2.StreamName("history/with spaces%2F?#")
	if _, err := client.Basin(string(basin)).Streams.Create(ctx, s2.CreateStreamArgs{Stream: name}); err != nil {
		t.Fatal(err)
	}
	return string(basin), string(name)
}

func testClient(endpoint string, compression s2.CompressionType) *s2.Client {
	token := os.Getenv("S2_FAULT_ACCESS_TOKEN")
	if token == "" {
		token = "test"
	}
	return s2.New(token, &s2.ClientOptions{
		BaseURL: endpoint, MakeBasinBaseURL: func(string) string { return endpoint },
		Compression: compression, RequestTimeout: 3 * time.Second,
		RetryConfig: &s2.RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: s2.AppendRetryPolicyNoSideEffects},
	})
}

func resultDir(t testing.TB) string {
	t.Helper()
	root := os.Getenv("S2_FAULT_OUTPUT")
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, strings.ReplaceAll(t.Name(), "/", "-")+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("artifacts: %s", dir)
		} else {
			_ = os.RemoveAll(dir)
		}
	})
	return dir
}
