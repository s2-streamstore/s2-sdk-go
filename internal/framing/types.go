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
// algorithms the client can decode, or "" when the client opted out via
// CompressionNone.
func (c CompressionType) AcceptEncoding() string {
	if c == CompressionNone {
		return ""
	}
	return "zstd, gzip"
}

// ParseContentEncoding parses a Content-Encoding header value into a
// CompressionType. Matching is case-insensitive, as HTTP content codings are
// case-insensitive (RFC 9110). For comma-separated lists, the first
// recognized coding wins; this protects callers that pair an explicit
// Accept-Encoding with raw error-body decoding, where a server may respond
// with a single coding in any casing. Unrecognized or absent codings yield
// CompressionNone, leaving the body untouched for the caller to surface.
func ParseContentEncoding(encoding string) CompressionType {
	for _, token := range strings.Split(encoding, ",") {
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "zstd":
			return CompressionZstd
		case "gzip":
			return CompressionGzip
		}
	}
	return CompressionNone
}
