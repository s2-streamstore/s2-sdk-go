/*
Go SDK for S2.

The Go SDK provides ergonomic wrappers and utilities to interact with the S2 API.

# Installation

	go get github.com/s2-streamstore/s2-sdk-go/s2@latest

# Authentication

Generate an authentication token by logging onto the web console at s2.dev.

# Quick Start

	package main

	import (
		"context"
		"fmt"
		"log"

		"github.com/s2-streamstore/s2-sdk-go/s2"
	)

	func main() {
		ctx := context.Background()
		client := s2.New("your-access-token", nil)

		stream := client.Basin("my-basin").Stream("my-stream")

		ack, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{
				{Body: []byte("first record")},
				{Body: []byte("second record")},
			},
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote records at seq %d\n", ack.Start.SeqNum)

		session, err := stream.ReadSession(ctx, &s2.ReadOptions{
			SeqNum: s2.Uint64(0),
		})
		if err != nil {
			log.Fatal(err)
		}
		defer session.Close()

		for session.Next(ctx) {
			rec := session.Record()
			fmt.Printf("[%d] %s\n", rec.SeqNum, string(rec.Body))
		}
		if err := session.Err(); err != nil {
			log.Fatal(err)
		}
	}

# How the SDK is Organized

The SDK has three levels of clients:

  - [Client] — managing basins, access tokens, metrics
  - [BasinClient] — managing streams within a basin
  - [StreamClient] — reading and writing records

The clients can be accessed like so:

	client := s2.New("token", nil)           // account level
	basin := client.Basin("my-basin")        // basin level
	stream := basin.Stream("my-stream")      // stream level

# Reading Records

Read session

	session, err := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),   // start from the beginning
		Wait:   s2.Int32(30),   // wait up to 30s for new records
	})
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	for session.Next(ctx) {
		rec := session.Record()
		// process rec
	}
	if err := session.Err(); err != nil {
		log.Fatal(err)
	}

	// resume from where you left off
	if pos := session.NextReadPosition(); pos != nil {
		fmt.Printf("next time, start from seq %d\n", pos.SeqNum)
	}

Unary Read

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
		Count:  s2.Uint64(100),
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, rec := range batch.Records {
		fmt.Printf("[%d] %s\n", rec.SeqNum, rec.Body)
	}

# Writing Records

Append Session (pipelining)

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	future, err := session.Submit(&s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("hello")}},
	})
	if err != nil {
		log.Fatal(err)
	}

	ticket, err := future.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("written at seq %d\n", ack.Start.SeqNum)

Unary Append

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("hello")},
			{Body: []byte("world")},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d records starting at seq %d\n",
		ack.End.SeqNum-ack.Start.SeqNum, ack.Start.SeqNum)

# Batching with Producer

Group records based on the amount collected or a linger time:

	session, _ := stream.AppendSession(ctx, nil)
	defer session.Close()

	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
		Linger:     100 * time.Millisecond,  // flush after 100ms
		MaxRecords: 500,                      // or after 500 records
	})
	producer := s2.NewProducer(ctx, batcher, session)
	defer producer.Close()

	for i := 0; i < 1000; i++ {
		future, _ := producer.Submit(s2.AppendRecord{
			Body: []byte(fmt.Sprintf("record %d", i)),
		})
		go func(f *s2.RecordSubmitFuture) {
			ticket, _ := f.Wait(ctx)
			ack, err := ticket.Ack(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)
				return
			}
			log.Printf("seq: %d", ack.SeqNum())
		}(future)
	}

# Managing Basins and Streams

Basins

	// Create a basin
	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: "my-basin",
		Scope: s2.Ptr(s2.BasinScopeAwsUsEast1),
	})

	// List basins
	iter := client.Basins.Iter(ctx, nil)
	for iter.Next() {
		fmt.Println(iter.Value().Name)
	}

	// Delete a basin
	client.Basins.Delete(ctx, "my-basin")

Streams

	basin := client.Basin("my-basin")

	// Create a stream
	info, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: "my-stream",
		Config: &s2.StreamConfig{
			StorageClass: s2.Ptr(s2.StorageClassExpress),
			RetentionPolicy: &s2.RetentionPolicy{
				Age: s2.Ptr(int64(86400 * 7)),  // keep for 7 days
			},
		},
	})

	// List streams
	iter := basin.Streams.Iter(ctx, nil)
	for iter.Next() {
		fmt.Println(iter.Value().Name)
	}

	// Delete a stream
	basin.Streams.Delete(ctx, "my-stream")

# Access Tokens

You can create scoped tokens for your applications:

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: "my-service",
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: s2.Ptr("prod-")},
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true, Write: true},
			},
		},
	})
	fmt.Printf("token: %s\n", resp.AccessToken)

	// Revoke
	client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: "my-service"})

# Error Handling

Errors from the SDK are typically [*S2Error] which includes the status code and
other info:

	ack, err := stream.Append(ctx, input)
	if err != nil {
		var s2Err *s2.S2Error
		if errors.As(err, &s2Err) {
			fmt.Printf("S2 error: %s (status %d, retryable: %v)\n",
				s2Err.Message, s2Err.Status, s2Err.IsRetryable())
		}
	}

[s2.dev]: https://s2.dev
[S2]: https://s2.dev
*/
package s2
