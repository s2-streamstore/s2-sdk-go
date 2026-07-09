package s2

import (
	"bytes"
	"testing"
)

// Benchmarks the per-batch cost of the deep clone that the Producer path
// previously paid in AppendSession.Submit on records the batcher already
// owned, versus validation alone (the submitOwned path).
func benchAppendInput() *AppendInput {
	records := make([]AppendRecord, 1000)
	body := bytes.Repeat([]byte{0xEF}, 1000)
	for i := range records {
		records[i] = AppendRecord{Body: body}
	}
	return &AppendInput{Records: records}
}

func BenchmarkPrepareAppendInputClone(b *testing.B) {
	input := benchAppendInput()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := prepareAppendInput(input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateAppendInput(b *testing.B) {
	input := benchAppendInput()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := validateAppendInput(input); err != nil {
			b.Fatal(err)
		}
	}
}
