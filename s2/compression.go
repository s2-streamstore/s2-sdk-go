package s2

import (
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

type CompressionType = internalframing.CompressionType

const (
	CompressionNone CompressionType = internalframing.CompressionNone
	CompressionZstd CompressionType = internalframing.CompressionZstd
	CompressionGzip CompressionType = internalframing.CompressionGzip
)
