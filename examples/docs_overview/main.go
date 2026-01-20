// Documentation examples for SDK Overview page.
//
// Run with: go run ./examples/docs_overview
// ANCHOR: create-client
package main

import (
	"fmt"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client := s2.NewFromEnvironment(nil)

	basin := client.Basin("my-basin")
	stream := basin.Stream("my-stream")

	fmt.Printf("Created client for stream: %+v\n", stream)
}
// ANCHOR_END: create-client
