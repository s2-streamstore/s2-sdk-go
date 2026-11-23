package framing

import (
	"bytes"
	"errors"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestDecompressedBodyAllowsStreamingZstdWindow(t *testing.T) {
	// Streaming zstd encoders default to an 8 MiB window (> MaxDecompressedSize)
	// even for small payloads. Decoding must still accept them while capping
	// decompressed output at MaxDecompressedSize.
	for _, size := range []int{64, MaxDecompressedSize, MaxDecompressedSize + 1} {
		var buf bytes.Buffer
		enc, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := enc.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}

		body, err := (&S2SFrame{Compression: CompressionZstd, Body: buf.Bytes()}).DecompressedBody()
		if size <= MaxDecompressedSize {
			if err != nil || len(body) != size {
				t.Fatalf("streaming zstd size %d: got %d bytes, err %v", size, len(body), err)
			}
			continue
		}
		if !errors.Is(err, ErrDecompressedTooLarge) {
			t.Fatalf("streaming zstd size %d: expected ErrDecompressedTooLarge, got %d bytes, err %v", size, len(body), err)
		}
	}
}
