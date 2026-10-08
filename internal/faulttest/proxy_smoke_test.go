package faulttest

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestLiteProxy(t *testing.T) {
	lite := newLite(t)
	basin, name := provision(t, lite.endpoint)
	proxy := newFaultProxy(t, lite.endpoint, make([]faultPlan, 1))
	stream := testClient(fmt.Sprintf("%s/op/0/v1", proxy.endpoint), s2.CompressionNone).Basin(basin).Stream(s2.StreamName(name))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := []byte("through-proxy")
	ack, err := stream.Append(ctx, &s2.AppendInput{Records: []s2.AppendRecord{{Body: body}}})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Start.SeqNum != 0 || ack.End.SeqNum != 1 {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	batch, err := stream.Read(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 || batch.Records[0].SeqNum != 0 || !bytes.Equal(batch.Records[0].Body, body) {
		t.Fatalf("unexpected records: %+v", batch.Records)
	}
}
