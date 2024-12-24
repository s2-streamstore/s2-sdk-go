package main

import (
	"context"
	"fmt"
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client, err := s2.NewClient(os.Getenv("S2_AUTH_TOKEN"))
	if err != nil {
		panic(err)
	}

	basins, err := client.ListBasins(context.TODO(), &s2.ListBasinsRequest{})
	if err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n", basins)
}
