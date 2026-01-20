// Documentation examples for Streams page.
//
// Run with: go run ./examples/docs_streams
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx := context.Background()
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		log.Fatal("S2_ACCESS_TOKEN is required")
	}
	basinName := os.Getenv("S2_BASIN")
	if basinName == "" {
		log.Fatal("S2_BASIN is required")
	}

	client := s2.New(token, nil)
	basin := client.Basin(basinName)

	// Create a temporary stream for examples
	streamName := fmt.Sprintf("docs-streams-%d", time.Now().UnixMilli())
	basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: s2.StreamName(streamName)})

	// ANCHOR: simple-append
	stream := basin.Stream(s2.StreamName(streamName))

	ack, _ := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("first event")},
			{Body: []byte("second event")},
		},
	})

	// ack tells us where the records landed
	fmt.Printf("Wrote records %d through %d\n", ack.Start.SeqNum, ack.End.SeqNum-1)
	// ANCHOR_END: simple-append

	// ANCHOR: simple-read
	batch, _ := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
		Count:  s2.Uint64(100),
	})

	for _, record := range batch.Records {
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	// ANCHOR_END: simple-read

	// ANCHOR: append-session
	session, _ := stream.AppendSession(ctx, nil)
	defer session.Close()

	// Submit a batch - this enqueues it and returns a ticket
	fut, _ := session.Submit(&s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("event-1")},
			{Body: []byte("event-2")},
		},
	})

	// Wait for enqueue (this is where backpressure happens)
	ticket, _ := fut.Wait(ctx)

	// Wait for durability
	ack2, _ := ticket.Ack(ctx)
	fmt.Printf("Durable at seqNum %d\n", ack2.Start.SeqNum)
	// ANCHOR_END: append-session

	// ANCHOR: producer
	session2, _ := stream.AppendSession(ctx, nil)
	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
		Linger: 5 * time.Millisecond,
	})
	producer := s2.NewProducer(ctx, batcher, session2)

	// Submit individual records
	fut2, _ := producer.Submit(s2.AppendRecord{Body: []byte("my event")})
	ticket2, _ := fut2.Wait(ctx)
	ack3, _ := ticket2.Ack(ctx)

	fmt.Printf("Record durable at seqNum %d\n", ack3.SeqNum())

	producer.Close()
	// ANCHOR_END: producer

	// ANCHOR: check-tail
	tail, _ := stream.CheckTail(ctx)
	fmt.Printf("Stream has %d records\n", tail.Tail.SeqNum)
	// ANCHOR_END: check-tail

	// Cleanup
	basin.Streams.Delete(ctx, s2.StreamName(streamName))

	fmt.Println("Streams examples completed")

	// The following read session examples are for documentation snippets only.
	// They are not executed because they would block waiting for new records.
	if os.Getenv("RUN_READ_SESSIONS") == "" {
		return
	}

	// ANCHOR: read-session
	readSession, _ := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
	})
	defer readSession.Close()

	for readSession.Next() {
		record := readSession.Record()
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	if err := readSession.Err(); err != nil {
		// Handle error
	}
	// ANCHOR_END: read-session

	// ANCHOR: read-session-tail-offset
	// Start reading from 10 records before the current tail
	tailOffsetSession, _ := stream.ReadSession(ctx, &s2.ReadOptions{
		TailOffset: s2.Int64(10),
	})
	defer tailOffsetSession.Close()

	for tailOffsetSession.Next() {
		record := tailOffsetSession.Record()
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	// ANCHOR_END: read-session-tail-offset

	// ANCHOR: read-session-timestamp
	// Start reading from a specific timestamp
	oneHourAgo := uint64(time.Now().Add(-time.Hour).UnixMilli())
	timestampSession, _ := stream.ReadSession(ctx, &s2.ReadOptions{
		Timestamp: &oneHourAgo,
	})
	defer timestampSession.Close()

	for timestampSession.Next() {
		record := timestampSession.Record()
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	// ANCHOR_END: read-session-timestamp

	// ANCHOR: read-session-until
	// Read records until a specific timestamp
	oneHourAgo = uint64(time.Now().Add(-time.Hour).UnixMilli())
	untilSession, _ := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
		Until:  &oneHourAgo,
	})
	defer untilSession.Close()

	for untilSession.Next() {
		record := untilSession.Record()
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	// ANCHOR_END: read-session-until

	// ANCHOR: read-session-wait
    // Read all available records, and once reaching the current tail, wait an additional 30 seconds for new ones
	waitSession, _ := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
		Wait:   s2.Int32(30),
	})
	defer waitSession.Close()

	for waitSession.Next() {
		record := waitSession.Record()
		fmt.Printf("[%d] %s\n", record.SeqNum, string(record.Body))
	}
	// ANCHOR_END: read-session-wait
}
