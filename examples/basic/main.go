package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
	"google.golang.org/grpc/grpclog"
)

func printStreamConfig(c *s2.StreamConfig) {
	fmt.Printf("storage class = %s\n", c.StorageClass)
	fmt.Printf("retention policy = %#v\n", c.RetentionPolicy)
}

func printBasinConfig(c *s2.BasinConfig) {
	fmt.Println("default stream config :-")
	if c.DefaultStreamConfig == nil {
		fmt.Println(nil)
	} else {
		printStreamConfig(c.DefaultStreamConfig)
	}
}

func run() error {
	client, err := s2.NewClient(
		os.Getenv("S2_AUTH_TOKEN"),
		// s2.WithEndpoints(&s2.Endpoints{
		// 	Account: "vrongmeal-macbook.prawn-typhon.ts.net:4243",
		// 	Basin:   "vrongmeal-macbook.prawn-typhon.ts.net:4243",
		// }),
	)
	if err != nil {
		return err
	}

	basinName := "vrongmeal-basin-3"

	basinInfo, err := client.CreateBasin(context.TODO(), &s2.CreateBasinRequest{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass:    s2.StorageClassStandard,
				RetentionPolicy: s2.RetentionPolicyAge(7 * time.Second),
			},
		},
	})
	if err != nil {
		return err
	}

	fmt.Printf("%#v\n\n", basinInfo)

	basinConfig, err := client.GetBasinConfig(context.TODO(), basinName)
	if err != nil {
		return err
	}

	printBasinConfig(basinConfig)
	println()

	updatedBasinConfig, err := client.ReconfigureBasin(context.TODO(), &s2.ReconfigureBasinRequest{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass:    s2.StorageClassUnspecified,
				RetentionPolicy: s2.RetentionPolicyAge(9 * time.Second),
			},
		},
	})
	if err != nil {
		return err
	}

	printBasinConfig(updatedBasinConfig)

	return nil
}

func main() {
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(os.Stdout, os.Stderr, os.Stderr))

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
