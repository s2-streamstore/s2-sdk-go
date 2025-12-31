package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := s2.NewFromEnvironment(nil)
	basin := client.Basin("<your-basin>")
	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: "<your-stream>"}); err != nil {
		log.Printf("create stream (may already exist): %v", err)
	}

	stream := basin.Stream("<your-stream>")
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{s2.NewHeader("example", "unary-demo")},
				Body:    []byte("hello from unary append"),
			},
		},
	})
	if err != nil {
		log.Fatalf("append failed: %v", err)
	}
	log.Printf("append: start=%d end=%d tail=%d", ack.Start.SeqNum, ack.End.SeqNum, ack.Tail.SeqNum)

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: &ack.Start.SeqNum,
		Count:  s2.Uint64(5),
	})
	if err != nil {
		log.Fatalf("read failed: %v", err)
	}

	log.Printf("read: %d record(s)", len(batch.Records))

	for _, rec := range batch.Records {
		fmt.Printf("  seq=%d body=%q headers=%q", rec.SeqNum, string(rec.Body), rec.Headers)
	}
}
