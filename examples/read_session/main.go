package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx := context.Background()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := s2.NewFromEnvironment(&s2.ClientOptions{
		IncludeBasinHeader: true,
		Logger:             logger,
		RequestTimeout:     12 * time.Second,
		RetryConfig: &s2.RetryConfig{
			MaxAttempts:       math.MaxInt,
			AppendRetryPolicy: s2.AppendRetryPolicyAll,
		},
	})

	basin := client.Basin("<your-basin>")
	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: "<your-stream>"}); err != nil {
		log.Printf("create stream (may already exist): %v", err)
	}

	stream := basin.Stream("<your-stream>")
	session, err := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
	})
	if err != nil {
		log.Fatalf("open read session: %v", err)
	}

	defer session.Close()

	for session.Next() {
		rec := session.Record()
		fmt.Printf("seq=%d body=%q\n", rec.SeqNum, string(rec.Body))
	}

	if err := session.Err(); err != nil {
		log.Printf("read error: %v", err)
	}

	if tail := session.LastObservedTail(); tail != nil {
		fmt.Printf("tail: seq=%d\n", tail.SeqNum)
	}

	if next := session.NextReadPosition(); next != nil {
		fmt.Printf("resume from: seq=%d\n", next.SeqNum)
	}
}
