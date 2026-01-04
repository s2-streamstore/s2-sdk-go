package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := s2.NewFromEnvironment(&s2.ClientOptions{
		BaseURL:            "http://localhost:4243",
		IncludeBasinHeader: true,
		MakeBasinBaseURL: func(basin string) string {
			return "http://localhost:4243/v1"
		},
		AllowH2C: true,
		// Logger:         logger,
		RequestTimeout: 30 * time.Second,
		RetryConfig: &s2.RetryConfig{
			MaxAttempts:       math.MaxInt,
			AppendRetryPolicy: s2.AppendRetryPolicyAll,
		},
	})

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: "hello-world",
	})
	basin := client.Basin("hello-world")

	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: "<your-stream>"}); err != nil {
		log.Printf("create stream (may already exist): %v", err)
	}

	stream := basin.Stream("<your-stream>")

	session, err := stream.AppendSession(ctx, &s2.AppendSessionOptions{
		MaxInflightBytes: 2000,
	})

	session2, err := stream.AppendSession(ctx, &s2.AppendSessionOptions{
		MaxInflightBytes: 2000,
	})
	if err != nil {
		log.Fatalf("open append session: %v", err)
	}

	defer session.Close()
	defer session2.Close()

	var wg sync.WaitGroup

	for i := range 10000 {
		body := fmt.Sprintf("message #%d", i+1)
		result, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(body)}},
		})

		result2, err := session2.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(body)}},
		})

		if err != nil {
			log.Printf("submit error: %v", err)

			break
		}

		wg.Add(1)

		// time.Sleep(5 * time.Second)

		go func() {
			defer wg.Done()

			ack, err := result.Wait(ctx)
			ack3, err := result2.Wait(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)

				return
			}

			ack2, err := ack.Ack(ctx)
			ack4, err := ack3.Ack(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)

				return
			}

			log.Printf("ack: end=%d tail=%d", ack2.End.SeqNum, ack2.Tail.SeqNum)
			log.Printf("ack: end=%d tail=%d", ack4.End.SeqNum, ack4.Tail.SeqNum)
		}()
	}

	wg.Wait()
}
