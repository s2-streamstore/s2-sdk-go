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
