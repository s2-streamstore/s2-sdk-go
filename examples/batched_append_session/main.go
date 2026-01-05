package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := s2.NewFromEnvironment(nil)
	basin := client.Basin("<your-basin>")
	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: "<your-stream>"}); err != nil {
		log.Printf("create stream (may already exist): %v", err)
	}

	stream := basin.Stream("<your-stream>")
	session, err := stream.AppendSession(ctx, nil)

	if err != nil {
		log.Fatalf("open append session: %v", err)
	}

	defer session.Close()

	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
		Linger:     1000 * time.Millisecond,
		MaxRecords: 100,
	})
	producer := s2.NewProducer(ctx, batcher, session)

	defer producer.Close()

	var wg sync.WaitGroup

	for i := range 5000 {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			fut, err := producer.Submit(s2.AppendRecord{
				Body: []byte(fmt.Sprintf("batched message #%d", n)),
			})
			if err != nil {
				log.Printf("submit error: %v", err)

				return
			}

			ticket, err := fut.Wait(ctx)
			if err != nil {
				log.Printf("enqueue error: %v", err)

				return
			}

			ack, err := ticket.Ack(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)

				return
			}
			log.Printf("ack: seqNum=%d", ack.SeqNum())
		}(i + 1)

		time.Sleep(5 * time.Millisecond)
	}

	wg.Wait()
}
