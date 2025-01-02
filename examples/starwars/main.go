package main

import (
	"bufio"
	"context"
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

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		// Copy tail into a new var since the arg accepts a ptr
		matchSeqNum := tail
		input := &s2.AppendInput{
			Records: []s2.AppendRecord{
				{
					Body: scanner.Bytes(),
				},
			},
			MatchSeqNum: &matchSeqNum,
		}
		if err := tx.Send(input); err != nil {
			return err
		}
		// Since we're sending 1 record in every batch
		tail++
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
	client, err := s2.NewStreamClient(
		os.Getenv("STARWARS_BASIN"),
		os.Getenv("STARWARS_STREAM"),
		os.Getenv("S2_AUTH_TOKEN"),
	)
	if err != nil {
		return fmt.Errorf("client create: %w", err)
	}

	tail, err := client.CheckTail(ctx)
	if err != nil {
		return fmt.Errorf("check tail: %w", err)
	}

	recRx, err := client.ReadSession(ctx, &s2.ReadSessionRequest{
		StartSeqNum: tail,
	})
	if err != nil {
		return err
	}

	appTx, appRx, err := client.AppendSession(ctx)
	if err != nil {
		return err
	}

	go func() {
		if err := append(appTx, tail); err != nil {
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
