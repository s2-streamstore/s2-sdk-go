package framing

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Returns the frame payload after applying the compression
// described in the frame header.
func (f *S2SFrame) DecompressedBody() ([]byte, error) {
	switch f.Compression {
	case CompressionNone:
		return f.Body, nil
	case CompressionGzip:
		return gunzip(f.Body)
	case CompressionZstd:
		return unzstd(f.Body)
	default:
		return nil, fmt.Errorf("unknown compression type %d", f.Compression)
	}
}

func gunzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return nil, fmt.Errorf("gzip decompress: %w", err)
	}
	return buf.Bytes(), nil
}

// Shared decoder for DecodeAll, which is safe for concurrent use and spawns
// no goroutines (those are streaming-only). Concurrency 0 (GOMAXPROCS) sets
// the number of pooled decoder states, bounding concurrent DecodeAll calls.
var zstdDecoder = func() *zstd.Decoder {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	if err != nil {
		panic(err)
	}
	return decoder
}()

func unzstd(data []byte) ([]byte, error) {
	out, err := zstdDecoder.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	return out, nil
}
