package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"

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

	tail, err := stream.CheckTail(ctx)
	if err != nil {
		return fmt.Errorf("check tail: %w", err)
	}

	readSession, err := stream.ReadSession(ctx, &s2.ReadOptions{
		SeqNum: &tail.Tail.SeqNum,
	})
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	defer readSession.Close()

	appendSession, err := stream.AppendSession(ctx, nil)
	if err != nil {
		return fmt.Errorf("append session: %w", err)
	}
	defer appendSession.Close()

	go func() {
		if err := appendFrames(ctx, appendSession); err != nil {
			fmt.Fprintln(os.Stderr, "Append error:", err)
		}
	}()

	for readSession.Next() {
		os.Stdout.Write(readSession.Record().Body)
		os.Stdout.Write([]byte("\r\n"))
	}

	return readSession.Err()
}

func appendFrames(ctx context.Context, session *s2.AppendSession) error {
	conn, err := net.Dial("tcp", "starwars.s2.dev:23")
	if err != nil {
		return err
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		fut, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: scanner.Bytes()}},
		})
		if err != nil {
			return err
		}

		go func() {
			ticket, err := fut.Wait(ctx)
			if err != nil {
				log.Printf("error enqueueing: %v", err)

				return
			}
			ack, err := ticket.Ack(ctx)
			if err != nil {
				log.Printf("error waiting: %v", err)

				return
			}

			b, _ := json.Marshal(ack)
			log.Printf("ack: %s", b)
		}()
	}

	return scanner.Err()
}
