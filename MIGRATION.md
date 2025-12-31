# Migration Guide from old to new s2 go sdk

## Client Initialization

### Before
```go
import "github.com/s2-streamstore/s2-sdk-go/s2"

client, err := s2.NewClient(
    authToken,
    s2.WithEndpoints(s2.EndpointsForCloud(s2.CloudAWS)),
    s2.WithConnectTimeout(3*time.Second),
    s2.WithMaxAttempts(3),
)
```

### After
```go
import "github.com/s2-streamstore/s2-sdk-go/s2"

client := s2.New(authToken, &s2.ClientOptions{
    BaseURL:           s2.DefaultBaseURL,
    ConnectionTimeout: 3 * time.Second,
    RetryConfig: &s2.RetryConfig{
        MaxAttempts: 3,
    },
})

// Or from environment variables (S2_ACCESS_TOKEN, S2_ACCOUNT_ENDPOINT, S2_BASIN_ENDPOINT)
client := s2.NewFromEnvironment(nil)
```

## Basin and Stream Clients

### Before
```go
basinClient, err := client.BasinClient("my-basin")
if err != nil {
    return err
}

streamClient := basinClient.StreamClient("my-stream")
```

### After
```go
basinClient := client.Basin("my-basin")
streamClient := basinClient.Stream("my-stream")
```

## Account Operations

### Before
```go
// List basins
resp, err := client.ListBasins(ctx, &s2.ListBasinsRequest{})

// Create basin
info, err := client.CreateBasin(ctx, &s2.CreateBasinRequest{
    Basin: "my-basin",
})
```

### After
```go
// List basins
iter := client.Basins.Iter(ctx, nil)
for iter.Next() {
    basin := iter.Value()
    // ...
}
if err := iter.Err(); err != nil {
    return err
}

// Create basin
info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
    Basin: "my-basin",
})
```

## Stream Operations

### Before
```go
// List streams
resp, err := basinClient.ListStreams(ctx, &s2.ListStreamsRequest{})

// Create stream
info, err := basinClient.CreateStream(ctx, &s2.CreateStreamRequest{
    Stream: "my-stream",
})
```

### After
```go
// List streams
iter := basinClient.Streams.Iter(ctx, nil)
for iter.Next() {
    stream := iter.Value()
    // ...
}

// Create stream
info, err := basinClient.Streams.Create(ctx, s2.CreateStreamArgs{
    Stream: "my-stream",
})
```

## Reading Records

### Before
```go
recv, err := streamClient.ReadSession(ctx, &s2.ReadSessionRequest{
    Start: s2.ReadStartSeqNum(0),
})
if err != nil {
    return err
}

for {
    output, err := recv.Recv()
    if err != nil {
        return err
    }

    switch o := output.(type) {
    case s2.ReadOutputBatch:
        for _, record := range o.Records {
            // process record
        }
    }
}
```

### After
```go
session, err := streamClient.ReadSession(ctx, &s2.ReadOptions{
    SeqNum: s2.Ptr(uint64(0)),
})
if err != nil {
    return err
}
defer session.Close()

for session.Next() {
    record := session.Record()
    // process record
}
if err := session.Err(); err != nil {
    return err
}
```

## Appending Records

### Unary Append

#### Before
```go
output, err := streamClient.Append(ctx, &s2.AppendInput{
    Records: []s2.AppendRecord{{Body: []byte("hello")}},
})
```

#### After
```go
output, err := streamClient.Append(ctx, &s2.AppendInput{
    Records: []s2.AppendRecord{{Body: []byte("hello")}},
})
```

### Streaming Append Session

#### Before
```go
sender, receiver, err := streamClient.AppendSession(ctx)
if err != nil {
    return err
}

// Send
err = sender.Send(&s2.AppendInput{
    Records: []s2.AppendRecord{{Body: []byte("hello")}},
})

// Receive acks
output, err := receiver.Recv()
```

#### After
```go
session, err := streamClient.AppendSession(ctx, nil)
if err != nil {
    return err
}
defer session.Close()

// Submit batch and wait for ack
future, err := session.Submit(&s2.AppendInput{
    Records: []s2.AppendRecord{{Body: []byte("hello")}},
})
if err != nil {
    return err
}

ticket, err := future.Wait(ctx)
if err != nil {
    return err
}

ack, err := ticket.Ack(ctx)
```

## Batching Records

### Before
```go
sender, _, err := streamClient.AppendSession(ctx)
if err != nil {
    return err
}

batchSender, err := s2.NewAppendRecordBatchingSender(
    sender,
    s2.WithLinger(5*time.Millisecond),
    s2.WithMatchSeqNum(0),
)
if err != nil {
    return err
}
defer batchSender.Close()

err = batchSender.Send(s2.AppendRecord{Body: []byte("hello")})
```

### After

**Option 1: Using Batcher directly**
```go
batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
    Linger:      5 * time.Millisecond,
    MatchSeqNum: s2.Ptr(uint64(0)),
})
defer batcher.Close()

// Add records
err := batcher.Add(s2.AppendRecord{Body: []byte("hello")}, nil)

// Consume batches
for batch := range batcher.Batches() {
    // Send batch.Input to append session
    session.Submit(batch.Input)
}
```

**Option 2: Using Producer (managed batching + session)**
```go
// Create batcher and session first
batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
    Linger: 5 * time.Millisecond,
})
session, err := streamClient.AppendSession(ctx, nil)
if err != nil {
    return err
}

// Producer ties them together
producer := s2.NewProducer(ctx, batcher, session)
defer producer.Close()

// Submit records - batching handled automatically
future, err := producer.Submit(s2.AppendRecord{Body: []byte("hello")})
if err != nil {
    return err
}

ticket, err := future.Wait(ctx)
if err != nil {
    return err
}

ack, err := ticket.Ack(ctx)
```
