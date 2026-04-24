// Documentation examples for Encryption page.
//
// Run with: go run ./examples/docs_encryption
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx := context.Background()
	accessToken := os.Getenv("S2_ACCESS_TOKEN")
	if accessToken == "" {
		log.Fatal("S2_ACCESS_TOKEN is required")
	}
	basinName := s2.BasinName(os.Getenv("S2_BASIN"))
	if basinName == "" {
		log.Fatal("S2_BASIN is required")
	}

	client := s2.New(accessToken, nil)
	basin := client.Basin(string(basinName))

	{
		// ANCHOR: basin-cipher
		client.Basins.Create(ctx, s2.CreateBasinArgs{
			Basin: basinName,
			Config: &s2.BasinConfig{
				StreamCipher: s2.Ptr(s2.EncryptionAlgorithmAegis256),
			},
		})

		client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
			Basin: basinName,
			Config: s2.BasinReconfiguration{
				StreamCipher: s2.Ptr(s2.EncryptionAlgorithmAes256Gcm),
			},
		})
		// ANCHOR_END: basin-cipher
	}

	streamName := fmt.Sprintf("docs-encryption-%d", time.Now().UnixMilli())
	basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: s2.StreamName(streamName)})

	{
		// ANCHOR: append-read
		key, err := s2.NewEncryptionKey(os.Getenv("S2_ENCRYPTION_KEY"))
		if err != nil {
			log.Fatalf("S2_ENCRYPTION_KEY: %v", err)
		}

		stream := basin.StreamWithOptions(s2.StreamName(streamName), &s2.StreamOptions{
			EncryptionKey: &key,
		})

		_, _ = stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{
				{Body: []byte("top secret")},
			},
		})

		batch, _ := stream.Read(ctx, &s2.ReadOptions{
			SeqNum: s2.Uint64(0),
			Count:  s2.Uint64(10),
		})
		// ANCHOR_END: append-read

		fmt.Printf("Encryption examples ok %d records\n", len(batch.Records))
	}

	// Cleanup
	basin.Streams.Delete(ctx, s2.StreamName(streamName))

	fmt.Println("Encryption examples completed")
}
