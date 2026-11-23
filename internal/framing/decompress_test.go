package framing

import (
	"errors"
	"testing"
)

func TestDecompressedBodyIsBounded(t *testing.T) {
	for _, compression := range []CompressionType{CompressionGzip, CompressionZstd} {
		for _, size := range []int{MaxDecompressedSize, MaxDecompressedSize + 1} {
			compressed, err := Compress(make([]byte, size), compression)
			if err != nil {
				t.Fatal(err)
			}
			body, err := (&S2SFrame{Compression: compression, Body: compressed}).DecompressedBody()
			if size <= MaxDecompressedSize {
				if err != nil || len(body) != size {
					t.Fatalf("compression %d, size %d: got %d bytes, err %v", compression, size, len(body), err)
				}
				continue
			}
			if !errors.Is(err, ErrDecompressedTooLarge) {
				t.Fatalf("compression %d, size %d: expected ErrDecompressedTooLarge, got %d bytes, err %v", compression, size, len(body), err)
			}
		}
	}
}
