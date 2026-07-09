package framing

import (
	"bytes"
	"testing"
)

func benchmarkCreateFrame(b *testing.B, size int, terminal bool) {
	data := bytes.Repeat([]byte{0xCD}, size)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = CreateFrameWithStatus(data, terminal, CompressionNone, 200)
	}
}

func BenchmarkCreateFrameSmall(b *testing.B)    { benchmarkCreateFrame(b, 512, false) }
func BenchmarkCreateFrameLarge(b *testing.B)    { benchmarkCreateFrame(b, 1024*1024, false) }
func BenchmarkCreateFrameTerminal(b *testing.B) { benchmarkCreateFrame(b, 512, true) }
