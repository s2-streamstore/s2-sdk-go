package s2

import (
	"math/rand"
	"testing"

	framing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

var appendFrameBenchmarkSink []byte

// BenchmarkAppendFrame measures conversion and framing with fresh protobuf messages.
func BenchmarkAppendFrame(b *testing.B) {
	cases := []struct {
		name        string
		records     int
		bodyBytes   int
		headerCount int
	}{
		{name: "1x128B", records: 1, bodyBytes: 128},
		{name: "1x4KiB", records: 1, bodyBytes: 4 * 1024},
		{name: "100x1KiB", records: 100, bodyBytes: 1024},
		{name: "1000x1KiB", records: 1000, bodyBytes: 1024},
		{name: "1xMaxBody", records: 1, bodyBytes: MaxBatchMeteredBytes - 8},
		{name: "1000x32B_4Headers", records: 1000, bodyBytes: 32, headerCount: 4},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			input := benchmarkAppendFrameInput(tc.records, tc.bodyBytes, tc.headerCount)
			if _, err := validateAppendInput(input); err != nil {
				b.Fatal(err)
			}
			wireBytes := int64(proto.Size(convertAppendInputToProto(input)) + 4)
			for _, direct := range []bool{false, true} {
				name := "Baseline"
				if direct {
					name = "Direct"
				}
				b.Run(name, func(b *testing.B) {
					b.SetBytes(wireBytes)
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						message := convertAppendInputToProto(input)
						var frame []byte
						var err error
						if direct {
							frame, err = framing.MarshalProtoFrame(message, framing.CompressionNone)
						} else {
							var payload []byte
							payload, err = proto.Marshal(message)
							if err == nil {
								frame = framing.CreateFrame(payload, false, framing.CompressionNone)
							}
						}
						if err != nil {
							b.Fatal(err)
						}
						appendFrameBenchmarkSink = frame
					}
				})
			}
		})
	}
}

func benchmarkAppendFrameInput(recordCount, bodyBytes, headerCount int) *AppendInput {
	rng := rand.New(rand.NewSource(1))
	records := make([]AppendRecord, recordCount)
	for i := range records {
		records[i].Body = make([]byte, bodyBytes)
		rng.Read(records[i].Body)
		for j := 0; j < headerCount; j++ {
			records[i].Headers = append(records[i].Headers, Header{
				Name:  []byte{byte('a' + j)},
				Value: []byte("value"),
			})
		}
	}
	return &AppendInput{Records: records}
}
