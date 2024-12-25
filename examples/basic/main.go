package main

import (
	"context"
	"fmt"
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client, err := s2.NewClient(
		os.Getenv("S2_AUTH_TOKEN"),
		// s2.WithRawEndpoints("aws.s2.dev", "aws.s2.dev"),
	)
	if err != nil {
		panic(err)
	}

	basins, err := client.ListBasins(context.TODO(), &s2.ListBasinsRequest{})
	if err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n", basins)

	basinClient, err := client.BasinClient("vrongmeal-basin")
	if err != nil {
		panic(err)
	}

	streams, err := basinClient.ListStreams(context.TODO(), &s2.ListStreamsRequest{})
	if err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n", streams)

	streamClient := basinClient.StreamClient("starwars2")

	tail, err := streamClient.CheckTail(context.TODO())
	if err != nil {
		panic(err)
	}

	fmt.Println("Tail:", tail)
}
