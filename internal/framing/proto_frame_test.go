package framing

import (
	"bytes"
	"fmt"
	"testing"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"google.golang.org/protobuf/proto"
)

func TestMarshalProtoFrameWireCompatibility(t *testing.T) {
	for _, size := range []int{0, 254, 255, 256, 1023, 1024, 1025, 65534, 65535, 65536, MaxFrameSize - 1} {
		message := appendProtoWithSize(t, size)
		for _, compression := range []CompressionType{CompressionNone, CompressionZstd, CompressionGzip} {
			t.Run(fmt.Sprintf("size=%d/compression=%d", size, compression), func(t *testing.T) {
				payload, err := proto.Marshal(message)
				if err != nil {
					t.Fatal(err)
				}
				want := CreateFrame(payload, false, compression)
				got, err := MarshalProtoFrame(message, compression)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatal("frame bytes differ from Marshal followed by CreateFrame")
				}

				frame, err := NewFrameReader(bytes.NewReader(got)).ReadFrame()
				if err != nil {
					t.Fatal(err)
				}
				if frame.Terminal || frame.ReconnectAdvised {
					t.Fatal("unexpected terminal or reconnect flag")
				}
				body, err := frame.DecompressedBody()
				if err != nil {
					t.Fatal(err)
				}
				var decoded pb.AppendInput
				if err := proto.Unmarshal(body, &decoded); err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(&decoded, message) {
					t.Fatal("decoded message differs from input")
				}
			})
		}
	}
}

func appendProtoWithSize(t *testing.T, size int) *pb.AppendInput {
	t.Helper()
	message := &pb.AppendInput{}
	if size == 0 {
		return message
	}
	// The nested record and body add two tags and two varint lengths.
	for overhead := 4; overhead <= 10; overhead++ {
		message.Records = []*pb.AppendRecord{{Body: bytes.Repeat([]byte{0xAB}, size-overhead)}}
		if proto.Size(message) == size {
			return message
		}
	}
	t.Fatalf("cannot construct AppendInput of encoded size %d", size)
	return nil
}

func TestMarshalProtoFrameOptionalFieldsAndResizing(t *testing.T) {
	zero := uint64(0)
	token := ""
	message := &pb.AppendInput{
		MatchSeqNum:  &zero,
		FencingToken: &token,
		Records: []*pb.AppendRecord{{
			Timestamp: &zero,
			Headers: []*pb.Header{
				{Name: nil, Value: []byte("fence")},
				{Name: []byte("name"), Value: []byte("value")},
			},
		}},
	}
	var first []byte
	for _, size := range []int{0, 128, 65536, 1} {
		message.Records[0].Body = bytes.Repeat([]byte{0xCD}, size)
		frame, err := MarshalProtoFrame(message, CompressionNone)
		if err != nil {
			t.Fatal(err)
		}
		var decoded pb.AppendInput
		if err := proto.Unmarshal(frame[4:], &decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(&decoded, message) {
			t.Fatalf("optional fields or resized payload changed at body size %d", size)
		}
		if size == 0 {
			first = frame
		}
	}
	var decoded pb.AppendInput
	if err := proto.Unmarshal(first[4:], &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Records[0].Body) != 0 {
		t.Fatal("later marshaling mutated an earlier frame")
	}
}

func TestMarshalProtoFrameRejectsInvalidUTF8(t *testing.T) {
	token := string([]byte{0xFF})
	message := &pb.AppendInput{FencingToken: &token}
	for _, compression := range []CompressionType{CompressionNone, CompressionZstd, CompressionGzip} {
		if _, err := MarshalProtoFrame(message, compression); err == nil {
			t.Fatalf("compression %d: expected invalid UTF-8 error", compression)
		}
	}
}
