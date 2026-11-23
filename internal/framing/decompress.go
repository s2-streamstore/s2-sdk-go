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

// Maximum zstd window accepted on the streaming fallback path (matches
// zstd's default streaming encoder window of 8 MiB).
const maxZstdWindowSize = 8 << 20

func unzstd(data []byte) ([]byte, error) {
	out, err := zstdDecoder.DecodeAll(data, nil)
	if errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return unzstdStream(data)
	}
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		return nil, ErrDecompressedTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	return out, nil
}

// unzstdStream handles valid streaming zstd frames whose declared window
// exceeds MaxDecompressedSize (e.g. zstd.NewWriter's default 8 MiB window)
// while bounding both the window and the decompressed output size.
func unzstdStream(data []byte) ([]byte, error) {
	reader, err := zstd.NewReader(
		bytes.NewReader(data),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(maxZstdWindowSize),
	)
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer reader.Close()

	out, err := io.ReadAll(io.LimitReader(reader, MaxDecompressedSize+1))
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	if len(out) > MaxDecompressedSize {
		return nil, ErrDecompressedTooLarge
	}
	return out, nil
}

// DecompressUnlimited decompresses a unary response body. Only session frames
// have a decompressed size limit; unary bodies are not bounded uncompressed
// either, and legitimate ones (such as long-range metrics) can exceed it.
func DecompressUnlimited(data []byte, compression CompressionType) ([]byte, error) {
	var reader io.ReadCloser
	switch compression {
	case CompressionNone:
		return data, nil
	case CompressionGzip:
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		reader = gz
	case CompressionZstd:
		zr, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("zstd reader: %w", err)
		}
		reader = zr.IOReadCloser()
	default:
		return nil, fmt.Errorf("unknown compression type: %d", compression)
	}
	defer reader.Close()
	out, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	return out, nil
}
