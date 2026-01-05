package s2_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func ExampleStreamClient_ReadSession() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0), // Start from beginning
		Wait:   s2.Int32(10), // Wait up to 10s for new records
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
		log.Fatalf("read session error: %v", err)
	}

	if next := session.NextReadPosition(); next != nil {
		fmt.Printf("resume from seq=%d\n", next.SeqNum)
	}
}

func ExampleStreamClient_AppendSession() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		log.Fatalf("open append session: %v", err)
	}
	defer session.Close()

	future, err := session.Submit(&s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("hello")},
			{Body: []byte("world")},
		},
	})
	if err != nil {
		log.Fatalf("submit: %v", err)
	}

	ticket, err := future.Wait(ctx)
	if err != nil {
		log.Fatalf("enqueue: %v", err)
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		log.Fatalf("get ack: %v", err)
	}

	fmt.Printf("appended records %d to %d\n", ack.Start.SeqNum, ack.End.SeqNum)
}

func ExampleProducer() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		log.Fatalf("open append session: %v", err)
	}
	defer session.Close()

	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
		Linger:     100 * time.Millisecond,
		MaxRecords: 500,
	})
	producer := s2.NewProducer(ctx, batcher, session)
	defer producer.Close()

	for i := range 100 {
		future, err := producer.Submit(s2.AppendRecord{
			Body: []byte(fmt.Sprintf("record %d", i)),
		})
		if err != nil {
			log.Fatalf("producer submit: %v", err)
		}

		go func(f *s2.RecordSubmitFuture) {
			ticket, err := f.Wait(ctx)
			if err != nil {
				log.Printf("enqueue error: %v", err)
				return
			}
			ack, err := ticket.Ack(ctx)
			if err != nil {
				log.Printf("ack error: %v", err)
				return
			}
			fmt.Printf("ack: seqNum=%d\n", ack.SeqNum())
		}(future)
	}

	time.Sleep(time.Second)
}

func ExampleBatchSubmitTicket_Ack() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		log.Fatalf("open append session: %v", err)
	}
	defer session.Close()

	future, err := session.Submit(&s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("first")},
			{Body: []byte("second")},
		},
	})
	if err != nil {
		log.Fatalf("submit append: %v", err)
	}

	ticket, err := future.Wait(ctx)
	if err != nil {
		log.Fatalf("enqueue failed: %v", err)
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		log.Fatalf("append failed: %v", err)
	}
	fmt.Printf("acknowledged records %d-%d\n", ack.Start.SeqNum, ack.End.SeqNum-1)
}

// Basins

func ExampleBasinsClient_Create() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: "my-new-basin",
		Scope: s2.Ptr(s2.BasinScopeAwsUsEast1),
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: s2.Ptr(s2.StorageClassStandard),
			},
		},
	})
	if err != nil {
		log.Fatalf("create basin: %v", err)
	}

	fmt.Printf("created basin: %s (state=%s)\n", info.Name, info.State)
}

func ExampleBasinsClient_List() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Prefix: "my-",
		Limit:  s2.Ptr(10),
	})
	if err != nil {
		log.Fatalf("list basins: %v", err)
	}

	for _, basin := range resp.Basins {
		fmt.Printf("basin: %s (scope=%s, state=%s)\n", basin.Name, basin.Scope, basin.State)
	}
	fmt.Printf("has more: %v\n", resp.HasMore)
}

func ExampleBasinsClient_Iter() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	iter := client.Basins.Iter(ctx, &s2.ListBasinsArgs{
		Prefix: "my-",
	})
	for iter.Next() {
		basin := iter.Value()
		fmt.Printf("basin: %s (scope=%s, state=%s)\n", basin.Name, basin.Scope, basin.State)
	}
	if err := iter.Err(); err != nil {
		log.Fatalf("list basins: %v", err)
	}
}

func ExampleBasinsClient_GetConfig() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	config, err := client.Basins.GetConfig(ctx, "my-basin")
	if err != nil {
		log.Fatalf("get basin config: %v", err)
	}

	fmt.Printf("basin config: %+v\n", config)
}

func ExampleBasinsClient_Reconfigure() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: "my-basin",
		Config: s2.BasinReconfiguration{
			CreateStreamOnAppend: s2.Ptr(true),
		},
	})
	if err != nil {
		log.Fatalf("reconfigure basin: %v", err)
	}

	fmt.Printf("basin config: %+v\n", config)
}

func ExampleBasinsClient_Delete() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	if err := client.Basins.Delete(ctx, "my-basin"); err != nil {
		log.Fatalf("delete basin: %v", err)
	}

	fmt.Println("basin deleted")
}

func ExampleStreamsClient_Create() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	info, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: "my-stream",
		Config: &s2.StreamConfig{
			StorageClass: s2.Ptr(s2.StorageClassExpress),
			RetentionPolicy: &s2.RetentionPolicy{
				Age: s2.Ptr(int64(86400 * 7)), // 7 days
			},
		},
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	fmt.Printf("created stream: %s\n", info.Name)
}

func ExampleStreamsClient_List() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix: "events-",
		Limit:  s2.Ptr(10),
	})
	if err != nil {
		log.Fatalf("list streams: %v", err)
	}

	for _, stream := range resp.Streams {
		fmt.Printf("stream: %s (created=%s)\n", stream.Name, stream.CreatedAt)
	}
	fmt.Printf("has more: %v\n", resp.HasMore)
}

func ExampleStreamsClient_Iter() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	iter := basin.Streams.Iter(ctx, &s2.ListStreamsArgs{
		Prefix: "events-",
	})
	for iter.Next() {
		stream := iter.Value()
		fmt.Printf("stream: %s (created=%s)\n", stream.Name, stream.CreatedAt)
	}
	if err := iter.Err(); err != nil {
		log.Fatalf("list streams: %v", err)
	}
}

func ExampleStreamsClient_GetConfig() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	config, err := basin.Streams.GetConfig(ctx, "my-stream")
	if err != nil {
		log.Fatalf("get stream config: %v", err)
	}

	fmt.Printf("stream config: %+v\n", config)
}

func ExampleStreamsClient_Reconfigure() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: "my-stream",
		Config: s2.StreamReconfiguration{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: s2.Ptr(int64(86400 * 30)),
			},
		},
	})
	if err != nil {
		log.Fatalf("reconfigure stream: %v", err)
	}

	fmt.Printf("stream config: %+v\n", config)
}

func ExampleStreamsClient_Delete() {
	client := s2.New("your-access-token", nil)
	basin := client.Basin("my-basin")
	ctx := context.Background()

	if err := basin.Streams.Delete(ctx, "my-stream"); err != nil {
		log.Fatalf("delete stream: %v", err)
	}

	fmt.Println("stream deleted")
}

func ExampleStreamClient_CheckTail() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")
	ctx := context.Background()

	tail, err := stream.CheckTail(ctx)
	if err != nil {
		log.Fatalf("check tail: %v", err)
	}

	fmt.Printf("tail: seq=%d ts=%d\n", tail.Tail.SeqNum, tail.Tail.Timestamp)
}

func ExampleStreamClient_Append() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")
	ctx := context.Background()

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("hello world")},
		},
	})
	if err != nil {
		log.Fatalf("append: %v", err)
	}

	fmt.Printf("appended at seq=%d\n", ack.Start.SeqNum)
}

func ExampleStreamClient_Read() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")
	ctx := context.Background()

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: s2.Uint64(0),
		Count:  s2.Uint64(100),
	})
	if err != nil {
		log.Fatalf("read: %v", err)
	}

	for _, rec := range batch.Records {
		fmt.Printf("seq=%d body=%q\n", rec.SeqNum, string(rec.Body))
	}
}

func ExampleNewFenceCommandRecord() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")
	ctx := context.Background()

	fenceRecord := s2.NewFenceCommandRecord("my-writer-id", nil)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{fenceRecord},
	})
	if err != nil {
		log.Fatalf("fence: %v", err)
	}

	fmt.Printf("fence set at seq=%d\n", ack.Start.SeqNum)
}

func ExampleNewTrimCommandRecord() {
	client := s2.New("your-access-token", nil)
	stream := client.Basin("my-basin").Stream("my-stream")
	ctx := context.Background()

	trimRecord := s2.NewTrimCommandRecord(1000, nil)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{trimRecord},
	})
	if err != nil {
		log.Fatalf("trim: %v", err)
	}

	fmt.Printf("trim command at seq=%d\n", ack.Start.SeqNum)
}

func ExampleAccessTokensClient_Issue() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: "my-service-token",
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: s2.Ptr("my-")},
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true, Write: true},
			},
		},
	})
	if err != nil {
		log.Fatalf("issue token: %v", err)
	}

	fmt.Printf("issued token: %s\n", resp.AccessToken)
}

func ExampleAccessTokensClient_List() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: "my-",
		Limit:  s2.Ptr(10),
	})
	if err != nil {
		log.Fatalf("list access tokens: %v", err)
	}

	for _, token := range resp.AccessTokens {
		fmt.Printf("token: %s\n", token.ID)
	}
	fmt.Printf("has more: %v\n", resp.HasMore)
}

func ExampleAccessTokensClient_Iter() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	iter := client.AccessTokens.Iter(ctx, nil)
	for iter.Next() {
		token := iter.Value()
		fmt.Printf("token: %s\n", token.ID)
	}
	if err := iter.Err(); err != nil {
		log.Fatalf("list tokens: %v", err)
	}
}

func ExampleAccessTokensClient_Revoke() {
	client := s2.New("your-access-token", nil)
	ctx := context.Background()

	if err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{
		ID: "my-service-token",
	}); err != nil {
		log.Fatalf("revoke token: %v", err)
	}

	fmt.Println("token revoked")
}

func ExampleMetricsClient_Account() {
	client := s2.New("your-access-token", nil)

	metrics, err := client.Metrics.Account(context.Background(), &s2.AccountMetricsArgs{
		Set: s2.AccountMetricSetActiveBasins,
	})
	if err != nil {
		log.Fatalf("account metrics: %v", err)
	}

	for _, m := range metrics.Values {
		if m.Scalar != nil {
			fmt.Printf("%s: %.0f %s\n", m.Scalar.Name, m.Scalar.Value, m.Scalar.Unit)
		}
	}
}

func ExampleMetricsClient_Basin() {
	client := s2.New("your-access-token", nil)

	metrics, err := client.Metrics.Basin(context.Background(), &s2.BasinMetricsArgs{
		Basin: "my-basin",
		Set:   s2.BasinMetricSetStorage,
	})
	if err != nil {
		log.Fatalf("basin metrics: %v", err)
	}

	for _, m := range metrics.Values {
		if m.Scalar != nil {
			fmt.Printf("%s: %.0f %s\n", m.Scalar.Name, m.Scalar.Value, m.Scalar.Unit)
		}
	}
}

func ExampleMetricsClient_Stream() {
	client := s2.New("your-access-token", nil)

	metrics, err := client.Metrics.Stream(context.Background(), &s2.StreamMetricsArgs{
		Basin:  "my-basin",
		Stream: "my-stream",
		Set:    s2.StreamMetricSetStorage,
	})
	if err != nil {
		log.Fatalf("stream metrics: %v", err)
	}

	for _, m := range metrics.Values {
		if m.Scalar != nil {
			fmt.Printf("%s: %.0f %s\n", m.Scalar.Name, m.Scalar.Value, m.Scalar.Unit)
		}
	}
}
