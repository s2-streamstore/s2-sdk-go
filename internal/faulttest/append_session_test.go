package faulttest

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestLiteAppendSessionNormalLifecycle(t *testing.T) {
	lite := newLite(t)
	for _, policy := range []s2.AppendRetryPolicy{s2.AppendRetryPolicyAll, s2.AppendRetryPolicyNoSideEffects} {
		for _, compression := range []struct {
			name string
			typ  s2.CompressionType
		}{{"none", s2.CompressionNone}, {"gzip", s2.CompressionGzip}, {"zstd", s2.CompressionZstd}} {
			t.Run(fmt.Sprintf("%s/%s", policy, compression.name), func(t *testing.T) {
				basin, name := provision(t, lite.endpoint)
				client := testClient(lite.endpoint, compression.typ)
				stream := client.Basin(basin).Stream(s2.StreamName(name))
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				session, err := stream.AppendSession(ctx, &s2.AppendSessionOptions{
					MaxInflightBatches: 8,
					RetryConfig: &s2.RetryConfig{
						MaxAttempts:       1,
						AppendRetryPolicy: policy,
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = session.Close() })

				// Send more batches than either the capacity or ACK channel
				// can hold, including multiple records and headers per batch.
				const batches = 64
				const recordsPerBatch = 3
				tickets := make([]*s2.BatchSubmitTicket, 0, batches)
				acks := make([]*s2.AppendAck, batches)
				bodies := make([][]byte, 0, batches*recordsPerBatch)
				for i := range batches {
					records := make([]s2.AppendRecord, recordsPerBatch)
					for j := range records {
						body := bytes.Repeat([]byte(fmt.Sprintf("batch-%d-record-%d;", i, j)), 100)
						records[j] = s2.AppendRecord{Body: body, Headers: []s2.Header{s2.NewHeader("kind", "lifecycle")}}
						bodies = append(bodies, body)
					}
					future, err := session.Submit(&s2.AppendInput{Records: records})
					if err != nil {
						t.Fatal(err)
					}
					ticket, err := future.Wait(ctx)
					if err != nil {
						t.Fatal(err)
					}
					tickets = append(tickets, ticket)
					// Drain halfway, then reuse the same idle session.
					if i == batches/2-1 {
						acks[i], err = ticket.Ack(ctx)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				// Close before waiting on the remaining ACKs; it must drain
				// them successfully even with retries completely disabled.
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
				for i, ticket := range tickets {
					if acks[i] == nil {
						acks[i], err = ticket.Ack(ctx)
						if err != nil {
							t.Fatal(err)
						}
					}
					ack := acks[i]
					if ack.Start.SeqNum != uint64(i*recordsPerBatch) || ack.End.SeqNum != uint64((i+1)*recordsPerBatch) {
						t.Fatalf("batch %d got out-of-order ACK: %+v", i, ack)
					}
				}
				tail, err := stream.CheckTail(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if tail.Tail.SeqNum != uint64(len(bodies)) {
					t.Fatalf("tail = %d, want %d (lost or duplicate records)", tail.Tail.SeqNum, len(bodies))
				}
				batch, err := stream.Read(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(0), Count: s2.Uint64(uint64(len(bodies)))})
				if err != nil {
					t.Fatal(err)
				}
				if len(batch.Records) != len(bodies) {
					t.Fatalf("read %d records, want %d", len(batch.Records), len(bodies))
				}
				for i, record := range batch.Records {
					if record.SeqNum != uint64(i) || !bytes.Equal(record.Body, bodies[i]) || len(record.Headers) != 1 || string(record.Headers[0].Value) != "lifecycle" {
						t.Fatalf("record %d did not round-trip: %+v", i, record)
					}
				}
			})
		}
	}
}
