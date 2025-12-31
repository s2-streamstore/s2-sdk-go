package framing

// CompressionType describes the frame-level compression encoding used for payloads.
type CompressionType uint8

const (
	CompressionNone CompressionType = 0
	CompressionZstd CompressionType = 1
	CompressionGzip CompressionType = 2
)
