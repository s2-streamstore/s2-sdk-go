package framing

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

func ParseContentEncoding(encoding string) CompressionType {
	switch encoding {
	case "zstd":
		return CompressionZstd
	case "gzip":
		return CompressionGzip
	default:
		return CompressionNone
	}
}
