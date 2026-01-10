package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	basin := flag.String("basin", "", "Basin name")
	stream := flag.String("stream", "", "Stream name")
	flag.Parse()

	if *basin == "" || *stream == "" {
		fmt.Fprintln(os.Stderr, "Usage: starwars -basin <name> -stream <name>")
		os.Exit(1)
	}

	if err := run(context.Background(), *basin, *stream); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, basinName, streamName string) error {
	client := s2.NewFromEnvironment(nil)
	stream := client.Basin(basinName).Stream(s2.StreamName(streamName))

	readSession, err := stream.ReadSession(ctx, &s2.ReadOptions{TailOffset: s2.Int64(0)})
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	defer readSession.Close()

	appendSession, err := stream.AppendSession(ctx, nil)
	if err != nil {
		return fmt.Errorf("append session: %w", err)
	}
	defer appendSession.Close()

	batcher := s2.NewBatcher(ctx, &s2.BatchingOptions{
		Linger:     5 * time.Millisecond,
		MaxRecords: 100,
	})
	producer := s2.NewProducer(ctx, batcher, appendSession)
	defer producer.Close()

	go func() {
		if err := appendFrames(ctx, producer); err != nil {
			fmt.Fprintln(os.Stderr, "Append error:", err)
		}
	}()

	for readSession.Next() {
		os.Stdout.Write(readSession.Record().Body)
	}

	return readSession.Err()
}

func appendFrames(ctx context.Context, producer *s2.Producer) error {
	conn, err := net.Dial("tcp", "starwars.s2.dev:23")
	if err != nil {
		return err
	}
	defer conn.Close()

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			producer.Submit(s2.AppendRecord{Body: chunk})
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
