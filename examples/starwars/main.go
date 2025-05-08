package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func append(tx s2.Sender[*s2.AppendInput], tail uint64) error {
	conn, err := net.Dial("tcp", "towel.blinkenlights.nl:23")
	if err != nil {
		return err
	}
	defer conn.Close()

	rtx, err := s2.NewAppendRecordBatchingSender(tx, s2.WithLinger(0), s2.WithMatchSeqNum(tail))
	if err != nil {
		return err
	}
	defer rtx.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		if err := rtx.Send(s2.AppendRecord{Body: scanner.Bytes()}); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func ack(rx s2.Receiver[*s2.AppendOutput]) error {
	for {
		if _, err := rx.Recv(); err != nil {
			return err
		}
	}
}

func read(rx s2.Receiver[s2.ReadOutput]) error {
	for {
		output, err := rx.Recv()
		if err != nil {
			return err
		}

		if batch, ok := output.(s2.ReadOutputBatch); ok {
			for _, record := range batch.Records {
				fmt.Println(string(record.Body))
			}
		}
	}
}

func run(ctx context.Context) error {
	basin := flag.String("basin", "", "Basin name")
	stream := flag.String("stream", "", "Stream name")

	authToken := os.Getenv("S2_AUTH_TOKEN")
	if authToken == "" {
		flag.StringVar(&authToken, "token", "", "S2 Authentication token")
	}

	flag.Parse()

	if *basin == "" {
		return fmt.Errorf("basin name cannot be empty")
	}

	if *stream == "" {
		return fmt.Errorf("stream name cannot be empty")
	}

	client, err := s2.NewStreamClient(
		*basin,
		*stream,
		authToken,
	)
	if err != nil {
		return fmt.Errorf("client create: %w", err)
	}

	tail, err := client.CheckTail(ctx)
	if err != nil {
		return fmt.Errorf("check tail: %w", err)
	}

	recRx, err := client.ReadSession(ctx, &s2.ReadSessionRequest{
		Start: s2.ReadStartSeqNum(tail.NextSeqNum),
	})
	if err != nil {
		return err
	}

	appTx, appRx, err := client.AppendSession(ctx)
	if err != nil {
		return err
	}

	go func() {
		if err := append(appTx, tail.NextSeqNum); err != nil {
			fmt.Fprintln(os.Stderr, "Append Error:", err)
			os.Exit(1)
		}
	}()

	go func() {
		if err := ack(appRx); err != nil {
			fmt.Fprintln(os.Stderr, "Append Ack Error:", err)
			os.Exit(1)
		}
	}()

	return read(recRx)
}

func main() {
	if err := run(context.TODO()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
}
