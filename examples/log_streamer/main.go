package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		slog.Error("S2_ACCESS_TOKEN environment variable is required")
		os.Exit(1)
	}

	basin := os.Getenv("S2_BASIN")
	if basin == "" {
		basin = "<your-basin>"
	}

	stream := os.Getenv("S2_STREAM")
	if stream == "" {
		stream = "<your-stream>"
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := s2.NewFromEnvironment(nil)
	streamClient := client.Basin(basin).Stream(s2.StreamName(stream))

	streamer, err := NewLogStreamer(context.Background(), logger, streamClient, LogStreamerConfig{
		BufferSize: 1000,
		BatchSize:  10,
		LingerTime: 1 * time.Second,
	})
	if err != nil {
		logger.Error("failed to create log streamer", "error", err)
		os.Exit(1)
	}

	if err := streamer.Start(); err != nil {
		logger.Error("failed to start log streamer", "error", err)
		os.Exit(1)
	}

	for i := range 20 {
		err := streamer.StreamLog(LogEntry{
			Level:     LogLevelInfo,
			Message:   "test log message",
			Component: "test",
			Fields: map[string]interface{}{
				"iteration": i,
			},
		})
		if err != nil {
			logger.Error("failed to stream log", "error", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(3 * time.Second)

	if err := streamer.Stop(); err != nil {
		logger.Error("failed to stop log streamer", "error", err)
	}

	logger.Info("done", "stats", streamer.GetStats())
}
