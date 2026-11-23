package framing

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// ErrDecompressedTooLarge reports a payload that decompresses past
// MaxDecompressedSize.
var ErrDecompressedTooLarge = fmt.Errorf("decompressed size exceeds %d bytes", MaxDecompressedSize)

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

	out, err := io.ReadAll(io.LimitReader(reader, MaxDecompressedSize+1))
	if err != nil {
		return nil, fmt.Errorf("gzip decompress: %w", err)
	}
	if len(out) > MaxDecompressedSize {
		return nil, ErrDecompressedTooLarge
	}
	return out, nil
}

// Shared decoder for DecodeAll, which is safe for concurrent use and spawns
// no goroutines (those are streaming-only). Concurrency 0 (GOMAXPROCS) sets
// the number of pooled decoder states, bounding concurrent DecodeAll calls.
var zstdDecoder = func() *zstd.Decoder {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(MaxDecompressedSize))
	if err != nil {
		panic(err)
	}
	return decoder
}()

// Fallback decoder for streaming zstd frames whose header declares a window
// larger than MaxDecompressedSize (e.g. zstd.NewWriter's default 8 MiB window).
// DecodeAll writes directly into dst without allocating WindowSize, and
// WithDecodeAllCapLimit enforces the MaxDecompressedSize output limit.
var zstdLargeWindowDecoder = func() *zstd.Decoder {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		panic(err)
	}
	return decoder
}()

func unzstd(data []byte) ([]byte, error) {
	out, err := zstdDecoder.DecodeAll(data, nil)
	if errors.Is(err, zstd.ErrWindowSizeExceeded) {
		out, err = zstdLargeWindowDecoder.DecodeAll(data, make([]byte, 0, MaxDecompressedSize))
		if err == nil && len(out) < cap(out)/2 {
			shrunk := make([]byte, len(out))
			copy(shrunk, out)
			out = shrunk
		}
	}
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		return nil, ErrDecompressedTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	return out, nil
}
