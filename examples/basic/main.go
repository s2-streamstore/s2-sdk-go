package main

import (
	"context"
	"fmt"
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client, err := s2.NewBasinClient(
		"vrongmeal-basin",
		os.Getenv("S2_AUTH_TOKEN"),
		s2.WithEndpoints(&s2.Endpoints{Account: "aws.s2.dev", Basin: "aws.s2.dev"}),
	)
	if err != nil {
		panic(err)
	}

	conf, err := client.GetStreamConfig(context.TODO(), "starwars2")
	if err != nil {
		panic(err)
	}

	fmt.Printf("%#v\n", conf)
}
