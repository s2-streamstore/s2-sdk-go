package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := s2.NewFromEnvironment(&s2.ClientOptions{
		Logger: logger,
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

	session, err := stream.AppendSession(ctx, &s2.AppendSessionOptions{
		MaxInflightBytes: 2000,
	})
	if err != nil {
		log.Fatalf("open append session: %v", err)
	}

	defer session.Close()

	var wg sync.WaitGroup

	for i := range 10000 {
		body := fmt.Sprintf("message #%d", i+1)
		result, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(body)}},
		})

		if err != nil {
			log.Printf("submit error: %v", err)

			break
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			fut, err := result.Wait(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)

				return
			}

			ack, err := fut.Ack(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)

				return
			}

			log.Printf("ack: end=%d tail=%d", ack.End.SeqNum, ack.Tail.SeqNum)
		}()
	}

	wg.Wait()
}
