package framing

import (
	"bytes"
	"io"
	"testing"
)

// chunkedReader returns at most chunkSize bytes per Read call, simulating a
// network stream that delivers data in pieces.
type chunkedReader struct {
	data      []byte
	chunkSize int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.chunkSize {
		n = r.chunkSize
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func benchmarkFrameReader(b *testing.B, bodySize, totalBytes, chunkSize int) {
	body := bytes.Repeat([]byte{0xAB}, bodySize)
	frame := CreateFrame(body, false, CompressionNone)

	var stream []byte
	for len(stream) < totalBytes {
		stream = append(stream, frame...)
	}

	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		fr := NewFrameReader(&chunkedReader{data: stream, chunkSize: chunkSize})
		for {
			_, err := fr.ReadFrame()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkFrameReaderSmallFrames(b *testing.B) {
	// 4 KB frames, 1 MB total, 16 KB network reads.
	benchmarkFrameReader(b, 4*1024, 1024*1024, 16*1024)
}

func BenchmarkFrameReaderMediumFrames(b *testing.B) {
	// 64 KB frames, 1 MB total, 16 KB network reads.
	benchmarkFrameReader(b, 64*1024, 1024*1024, 16*1024)
}

func BenchmarkFrameReaderLargeFrame(b *testing.B) {
	// Single ~2 MiB frame, 16 KB network reads.
	benchmarkFrameReader(b, MaxFrameSize-1, MaxFrameSize, 16*1024)
}
