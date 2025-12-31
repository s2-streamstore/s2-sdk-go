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

func unzstd(data []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer decoder.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, decoder); err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	return buf.Bytes(), nil
}
