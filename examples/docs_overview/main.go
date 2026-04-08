// Documentation examples for SDK Overview page.
//
// Run with: go run ./examples/docs_overview
// ANCHOR: create-client
package main

import (
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client := s2.New(os.Getenv("S2_ACCESS_TOKEN"), nil)

	basin := client.Basin("my-basin")
	_ = basin.Stream("my-stream")
}
// ANCHOR_END: create-client
