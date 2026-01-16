package bentobox_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
	"github.com/s2-streamstore/s2-sdk-go/s2-bentobox"
)

const testTimeout = 60 * time.Second

func getAccessToken(t *testing.T) string {
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set, skipping integration test")
	}
	return token
}

func newTestClient(t *testing.T) *s2.Client {
	return s2.NewFromEnvironment(nil)
}

func uniqueBasinName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func createTestBasin(ctx context.Context, t *testing.T, client *s2.Client, name string) {
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: s2.BasinName(name),
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: s2.Ptr(true),
			CreateStreamOnRead:   s2.Ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Failed to create basin: %v", err)
	}
	t.Logf("Created basin: %s", name)
}

func deleteTestBasin(ctx context.Context, t *testing.T, client *s2.Client, name string) {
	err := client.Basins.Delete(ctx, s2.BasinName(name))
	if err != nil {
		t.Logf("Warning: failed to delete basin %s: %v", name, err)
	} else {
		t.Logf("Deleted basin: %s", name)
	}
}

func createTestStream(ctx context.Context, t *testing.T, client *s2.Client, basinName, streamName string) {
	basin := client.Basin(basinName)
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: s2.StreamName(streamName),
	})
	if err != nil {
		t.Fatalf("Failed to create stream %s: %v", streamName, err)
	}
	t.Logf("Created stream: %s", streamName)
}

// noopLogger implements bentobox.Logger with no-op methods
type noopLogger struct{}

func (l *noopLogger) Tracef(template string, args ...any) {}
func (l *noopLogger) Trace(message string)                {}
func (l *noopLogger) Debugf(template string, args ...any) {}
func (l *noopLogger) Debug(message string)                {}
func (l *noopLogger) Infof(template string, args ...any)  {}
func (l *noopLogger) Info(message string)                 {}
func (l *noopLogger) Warnf(template string, args ...any)  {}
func (l *noopLogger) Warn(message string)                 {}
func (l *noopLogger) Errorf(template string, args ...any) {}
func (l *noopLogger) Error(message string)                {}
func (l *noopLogger) With(keyValuePairs ...any) bentobox.Logger {
	return l
}

// noopCache implements bentobox.SeqNumCache with in-memory storage
type noopCache struct {
	data map[string]uint64
}

func newNoopCache() *noopCache {
	return &noopCache{data: make(map[string]uint64)}
}

func (c *noopCache) Get(ctx context.Context, stream string) (uint64, error) {
	return c.data[stream], nil
}

func (c *noopCache) Set(ctx context.Context, stream string, seqNum uint64) error {
	c.data[stream] = seqNum
	return nil
}

func TestOutputAndInput_RoundTrip(t *testing.T) {
	accessToken := getAccessToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	client := newTestClient(t)
	basinName := uniqueBasinName("bentobox-test")
	streamName := "test-stream"

	createTestBasin(ctx, t, client, basinName)
	defer deleteTestBasin(ctx, t, client, basinName)
	time.Sleep(2 * time.Second)
	createTestStream(ctx, t, client, basinName, streamName)

	config := &bentobox.Config{
		Basin:       basinName,
		AccessToken: accessToken,
	}

	t.Run("Output", func(t *testing.T) {
		output, err := bentobox.ConnectOutput(ctx, &bentobox.OutputConfig{
			Config:      config,
			Stream:      streamName,
			MaxInFlight: 10,
		})
		if err != nil {
			t.Fatalf("ConnectOutput failed: %v", err)
		}
		defer output.Close(ctx)

		for i := 0; i < 5; i++ {
			records := []s2.AppendRecord{
				{
					Body: []byte(fmt.Sprintf("test message %d", i)),
					Headers: []s2.Header{
						{Name: []byte("index"), Value: []byte(fmt.Sprintf("%d", i))},
					},
				},
			}
			if err := output.WriteBatch(ctx, records); err != nil {
				t.Fatalf("WriteBatch failed: %v", err)
			}
			t.Logf("Wrote message %d", i)
		}
	})

	t.Run("Input", func(t *testing.T) {
		input, err := bentobox.ConnectMultiStreamInput(ctx, &bentobox.InputConfig{
			Config:      config,
			Streams:     bentobox.StaticInputStreams{Streams: []string{streamName}},
			MaxInFlight: 10,
			Cache:       newNoopCache(),
			Logger:      &noopLogger{},
			StartSeqNum: bentobox.InputStartSeqNumEarliest,
		})
		if err != nil {
			t.Fatalf("ConnectMultiStreamInput failed: %v", err)
		}
		defer input.Close(ctx)

		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		defer readCancel()

		messagesRead := 0
		for messagesRead < 5 {
			records, ackFn, stream, err := input.ReadBatch(readCtx)
			if err != nil {
				t.Fatalf("ReadBatch failed: %v", err)
			}

			if stream != streamName {
				t.Errorf("Expected stream %s, got %s", streamName, stream)
			}

			for _, record := range records {
				t.Logf("Read message: %s (seq=%d)", string(record.Body), record.SeqNum)
				messagesRead++
			}

			if err := ackFn(ctx, nil); err != nil {
				t.Errorf("Ack failed: %v", err)
			}
		}

		if messagesRead < 5 {
			t.Errorf("Expected to read 5 messages, got %d", messagesRead)
		}
	})
}

func TestOutput_WriteBatch(t *testing.T) {
	accessToken := getAccessToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	client := newTestClient(t)
	basinName := uniqueBasinName("bentobox-out")
	streamName := "output-test"

	createTestBasin(ctx, t, client, basinName)
	defer deleteTestBasin(ctx, t, client, basinName)

	time.Sleep(2 * time.Second)
	createTestStream(ctx, t, client, basinName, streamName)

	config := &bentobox.Config{
		Basin:       basinName,
		AccessToken: accessToken,
	}

	output, err := bentobox.ConnectOutput(ctx, &bentobox.OutputConfig{
		Config:      config,
		Stream:      streamName,
		MaxInFlight: 5,
	})
	if err != nil {
		t.Fatalf("ConnectOutput failed: %v", err)
	}
	defer output.Close(ctx)

	records := make([]s2.AppendRecord, 10)
	for i := range records {
		records[i] = s2.AppendRecord{
			Body: []byte(fmt.Sprintf("batch record %d", i)),
		}
	}

	if err := output.WriteBatch(ctx, records); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	t.Logf("Successfully wrote batch of %d records", len(records))
}

func TestInput_PrefixedStreams(t *testing.T) {
	accessToken := getAccessToken(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	client := newTestClient(t)
	basinName := uniqueBasinName("bentobox-pfx")
	prefix := "prefix-test/"

	createTestBasin(ctx, t, client, basinName)
	defer deleteTestBasin(ctx, t, client, basinName)

	time.Sleep(2 * time.Second)

	config := &bentobox.Config{
		Basin:       basinName,
		AccessToken: accessToken,
	}

	for i := 0; i < 3; i++ {
		streamName := fmt.Sprintf("%sstream-%d", prefix, i)
		createTestStream(ctx, t, client, basinName, streamName)

		output, err := bentobox.ConnectOutput(ctx, &bentobox.OutputConfig{
			Config: config,
			Stream: streamName,
		})
		if err != nil {
			t.Fatalf("ConnectOutput failed for %s: %v", streamName, err)
		}

		records := []s2.AppendRecord{
			{Body: []byte(fmt.Sprintf("message from stream %d", i))},
		}
		if err := output.WriteBatch(ctx, records); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}
		output.Close(ctx)
		t.Logf("Wrote to stream: %s", streamName)
	}

	input, err := bentobox.ConnectMultiStreamInput(ctx, &bentobox.InputConfig{
		Config:                config,
		Streams:               bentobox.PrefixedInputStreams{Prefix: prefix},
		MaxInFlight:           10,
		Cache:                 newNoopCache(),
		Logger:                &noopLogger{},
		UpdateStreamsInterval: 1 * time.Second,
		StartSeqNum:           bentobox.InputStartSeqNumEarliest,
	})
	if err != nil {
		t.Fatalf("ConnectMultiStreamInput failed: %v", err)
	}
	defer input.Close(ctx)

	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	defer readCancel()

	streamsRead := make(map[string]bool)
	for len(streamsRead) < 3 {
		records, ackFn, stream, err := input.ReadBatch(readCtx)
		if err != nil {
			t.Fatalf("ReadBatch failed: %v", err)
		}

		streamsRead[stream] = true
		for _, record := range records {
			t.Logf("Read from %s: %s", stream, string(record.Body))
		}

		if err := ackFn(ctx, nil); err != nil {
			t.Errorf("Ack failed: %v", err)
		}
	}

	t.Logf("Successfully read from %d streams", len(streamsRead))
}
