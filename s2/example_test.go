package s2_test

import (
	"context"
	"fmt"
	"time"

	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func ExampleClient_CreateBasin() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	basinName := "my-basin"

	defaultStreamConfig := s2.StreamConfig{
		// Set default basin retention policy to 10 days
		RetentionPolicy: s2.RetentionPolicyAge(10 * 24 * time.Hour),
	}

	basinConfig := s2.BasinConfig{
		DefaultStreamConfig: &defaultStreamConfig,
	}

	createdBasinInfo, err := client.CreateBasin(context.TODO(), &s2.CreateBasinRequest{
		Basin:  basinName,
		Config: &basinConfig,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(createdBasinInfo)
}

func ExampleBasinClient_CreateStream() {
	basinClient, err := s2.NewBasinClient("my-basin", "my-auth-token")
	if err != nil {
		panic(err)
	}

	streamName := "my-stream"

	streamConfig := s2.StreamConfig{
		StorageClass: s2.StorageClassExpress,
	}

	createdStreamInfo, err := basinClient.CreateStream(context.TODO(), &s2.CreateStreamRequest{
		Stream: streamName,
		Config: &streamConfig,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(createdStreamInfo)
}

func ExampleClient_DeleteBasin() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	basinName := "my-basin"

	if err := client.DeleteBasin(context.TODO(), &s2.DeleteBasinRequest{
		Basin: basinName,
		// Don't error if the basin doesn't exist.
		IfExists: true,
	}); err != nil {
		panic(err)
	}

	fmt.Println("Basin deletion requested")
}

func ExampleBasinClient_DeleteStream() {
	basinClient, err := s2.NewBasinClient("my-basin", "my-auth-token")
	if err != nil {
		panic(err)
	}

	streamName := "my-stream"

	if err := basinClient.DeleteStream(context.TODO(), &s2.DeleteStreamRequest{
		Stream: streamName,
		// Don't error if the basin doesn't exist.
		IfExists: true,
	}); err != nil {
		panic(err)
	}

	fmt.Println("Stream deletion requested")
}

func ExampleClient_ListBasins() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	var (
		allBasins  []s2.BasinInfo
		startAfter string
		hasMore    = true
	)

	for hasMore {
		listBasinsResponse, err := client.ListBasins(context.TODO(), &s2.ListBasinsRequest{
			StartAfter: startAfter,
		})
		if err != nil {
			panic(err)
		}

		allBasins = append(allBasins, listBasinsResponse.Basins...)

		startAfter = allBasins[len(allBasins)-1].Name
		hasMore = listBasinsResponse.HasMore
	}

	fmt.Println(allBasins)
}

func ExampleBasinClient_ListStreams() {
	basinClient, err := s2.NewBasinClient("my-basin", "my-auth-token")
	if err != nil {
		panic(err)
	}

	prefix := "my-"

	listStreamsResponse, err := basinClient.ListStreams(context.TODO(), &s2.ListStreamsRequest{
		Prefix: prefix,
		Limit:  optr.Some(uint64(10)),
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(listStreamsResponse)
}

func ExampleClient_ReconfigureBasin() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	basinName := "my-basin"

	defaultStreamConfigUpdates := s2.StreamConfig{
		StorageClass: s2.StorageClassStandard,
	}

	basinConfigUpdates := s2.BasinConfig{
		DefaultStreamConfig: &defaultStreamConfigUpdates,
	}

	updatedConfig, err := client.ReconfigureBasin(context.TODO(), &s2.ReconfigureBasinRequest{
		Basin:  basinName,
		Config: &basinConfigUpdates,
		// Only update storage class for the basin config
		Mask: []string{"default_stream_config.storage_class"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(updatedConfig)
}

func ExampleBasinClient_ReconfigureStream() {
	basinClient, err := s2.NewBasinClient("my-basin", "my-auth-token")
	if err != nil {
		panic(err)
	}

	streamName := "my-stream"

	streamConfigUpdates := s2.StreamConfig{
		RetentionPolicy: s2.RetentionPolicyAge(24 * time.Hour), // 1 Day
	}

	updatedConfig, err := basinClient.ReconfigureStream(context.TODO(), &s2.ReconfigureStreamRequest{
		Stream: streamName,
		Config: &streamConfigUpdates,
		// Only update retention policy for the stream config
		Mask: []string{"retention_policy"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(updatedConfig)
}

func ExampleClient_GetBasinConfig() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	basinName := "my-basin"

	config, err := client.GetBasinConfig(context.TODO(), basinName)
	if err != nil {
		panic(err)
	}

	fmt.Println(config)
}

func ExampleBasinClient_GetStreamConfig() {
	basinClient, err := s2.NewBasinClient("my-basin", "my-auth-token")
	if err != nil {
		panic(err)
	}

	streamName := "my-stream"

	config, err := basinClient.GetStreamConfig(context.TODO(), streamName)
	if err != nil {
		panic(err)
	}

	fmt.Println(config)
}

func ExampleCommandRecordTrim() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	tail, err := streamClient.CheckTail(context.TODO())
	if err != nil {
		panic(err)
	}

	if tail.NextSeqNum == 0 {
		fmt.Println("Empty stream")

		return
	}

	latestSeqNum := tail.NextSeqNum - 1

	command := s2.CommandRecordTrim{
		SeqNum: latestSeqNum,
	}

	batch, _ := s2.NewAppendRecordBatch(s2.AppendRecordFromCommand(command))

	if _, err = streamClient.Append(context.TODO(), &s2.AppendInput{
		Records: batch,
	}); err != nil {
		panic(err)
	}

	fmt.Println("Trim requested")
}

func ExampleCommandRecordFence() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	fencingToken := s2.GenerateFencingToken(16)

	command := s2.CommandRecordFence{
		FencingToken: fencingToken,
	}

	batch, _ := s2.NewAppendRecordBatch(s2.AppendRecordFromCommand(command))

	if _, err = streamClient.Append(context.TODO(), &s2.AppendInput{
		Records: batch,
	}); err != nil {
		panic(err)
	}

	fmt.Println("Fencing token set")
}

func ExampleStreamClient_CheckTail() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	tail, err := streamClient.CheckTail(context.TODO())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Tail of the stream is at %d\n", tail)
}

func ExampleStreamClient_Append() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	tail, err := streamClient.CheckTail(context.TODO())
	if err != nil {
		panic(err)
	}

	batch, _ := s2.NewAppendRecordBatch(
		s2.AppendRecord{Body: []byte("record 1")},
		s2.AppendRecord{Body: []byte("record 2")},
		// ...
	)

	ack, err := streamClient.Append(context.TODO(), &s2.AppendInput{
		Records:     batch,
		MatchSeqNum: &tail.NextSeqNum,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(ack)
}

func ExampleStreamClient_Read() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	latestBatch, err := streamClient.Read(context.TODO(), &s2.ReadRequest{
		Start: s2.ReadStartTailOffset(1),
		Limit: s2.ReadLimit{Count: optr.Some(uint64(1))},
	})
	if err != nil {
		panic(err)
	}

	latestRecord := latestBatch.(s2.ReadOutputBatch).Records[0]
	fmt.Println(latestRecord)
}

func ExampleStreamClient_AppendSession() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	sender, receiver, err := streamClient.AppendSession(context.TODO())
	if err != nil {
		panic(err)
	}

	records := []s2.AppendRecord{
		{Body: []byte("my record 1")},
		{Body: []byte("my record 2")},
		// ...
	}

	fencingToken := "my-fencing-token"
	recordSender, err := s2.NewAppendRecordBatchingSender(
		sender,
		s2.WithFencingToken(&fencingToken),
		s2.WithMaxBatchRecords(100),
	)
	if err != nil {
		panic(err)
	}
	defer recordSender.Close()

	send := make(chan error)

	go func() {
		defer close(send)

		for _, record := range records {
			if err := recordSender.Send(record); err != nil {
				send <- err

				return
			}
		}
	}()

	ack := make(chan error)

	go func() {
		defer close(ack)

		for {
			resp, err := receiver.Recv()
			if err != nil {
				ack <- err

				return
			}

			fmt.Printf("Appended: %#v\n", resp)
		}
	}()

	select {
	case err := <-send:
		panic(fmt.Errorf("send error: %w", err))
	case err := <-ack:
		panic(fmt.Errorf("ack error: %w", err))
	}
}

func ExampleStreamClient_ReadSession() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	receiver, err := streamClient.ReadSession(context.TODO(), &s2.ReadSessionRequest{
		// Read from the beginning
		Start: s2.ReadStartSeqNum(0),
	})
	if err != nil {
		panic(err)
	}

	for {
		next, err := receiver.Recv()
		if err != nil {
			panic(err)
		}

		fmt.Println(next)
	}
}

func ExampleClient_IssueAccessToken() {
	client, err := s2.NewClient("my-auth-token")
	if err != nil {
		panic(err)
	}

	tokenScope := &s2.AccessTokenScope{
		Basins:  s2.ResourceSetPrefix("my-"),
		Streams: s2.ResourceSetPrefix(""),
		OpGroups: &s2.PermittedOperationGroups{
			Stream: &s2.ReadWritePermissions{
				Read:  true,
				Write: true,
			},
		},
	}

	tokenInfo := &s2.AccessTokenInfo{
		ID:                "my-access-token-id",
		ExpiresAt:         optr.Some(time.Now().Add(24 * time.Hour)),
		AutoPrefixStreams: false,
		Scope:             tokenScope,
	}

	accessToken, err := client.IssueAccessToken(context.TODO(), tokenInfo)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created access token: %s\n", accessToken)
}

func ExampleAppendRecordBatchingSender() {
	streamClient, err := s2.NewStreamClient("my-basin", "my-stream", "my-auth-token")
	if err != nil {
		panic(err)
	}

	sender, _, err := streamClient.AppendSession(context.TODO())
	if err != nil {
		panic(err)
	}

	fencingToken := "my-fencing-token"
	recordSender, err := s2.NewAppendRecordBatchingSender(
		sender,
		s2.WithFencingToken(&fencingToken),
		s2.WithMaxBatchRecords(100),
	)
	if err != nil {
		panic(err)
	}
	defer recordSender.Close()

	records := []s2.AppendRecord{
		{Body: []byte("my record 1")},
		{Body: []byte("my record 2")},
		// ...
	}

	for _, record := range records {
		if err := recordSender.Send(record); err != nil {
			panic(err)
		}
	}
}
