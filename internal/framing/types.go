package framing

import "strings"

// CompressionType describes the frame-level compression encoding used for payloads.
type CompressionType uint8

const (
	CompressionNone CompressionType = 0
	CompressionZstd CompressionType = 1
	CompressionGzip CompressionType = 2
)

func (c CompressionType) ContentEncoding() string {
	switch c {
	case CompressionZstd:
		return "zstd"
	case CompressionGzip:
		return "gzip"
	default:
		return ""
	}
}

// AcceptEncoding returns the Accept-Encoding header value advertising
// only the configured compression algorithm, or "" when CompressionNone is set.
func (c CompressionType) AcceptEncoding() string {
	return c.ContentEncoding()
}

func ParseContentEncoding(encoding string) CompressionType {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "zstd":
		return CompressionZstd
	case "gzip":
		return CompressionGzip
	default:
		return CompressionNone
	}
}
