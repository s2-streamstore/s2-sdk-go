package s2

import (
	"bytes"
	"testing"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"google.golang.org/protobuf/proto"
)

func TestConvertAppendInputToProto(t *testing.T) {
	cases := []struct {
		name  string
		input *AppendInput
		want  *pb.AppendInput
	}{
		{name: "nil"},
		{name: "empty", input: &AppendInput{}, want: &pb.AppendInput{}},
		{
			name: "mixed records",
			input: &AppendInput{
				MatchSeqNum:  Uint64(0),
				FencingToken: String(""),
				Records: []AppendRecord{
					{Timestamp: Uint64(0), Headers: []Header{{Value: []byte("fence")}}, Body: []byte("token")},
					{Body: []byte{}, Headers: []Header{}},
					{Timestamp: Uint64(123), Headers: []Header{
						{Name: []byte("first"), Value: []byte{0, 255}},
						{Name: []byte("second"), Value: []byte{}},
					}, Body: []byte{128, 0, 255}},
					{},
				},
			},
			want: &pb.AppendInput{
				MatchSeqNum:  Uint64(0),
				FencingToken: String(""),
				Records: []*pb.AppendRecord{
					{Timestamp: Uint64(0), Headers: []*pb.Header{{Value: []byte("fence")}}, Body: []byte("token")},
					{},
					{Timestamp: Uint64(123), Headers: []*pb.Header{
						{Name: []byte("first"), Value: []byte{0, 255}},
						{Name: []byte("second"), Value: []byte{}},
					}, Body: []byte{128, 0, 255}},
					{},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convertAppendInputToProto(tc.input)
			if !proto.Equal(got, tc.want) {
				t.Fatalf("converted input = %v, want %v", got, tc.want)
			}
			encoded, err := proto.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			want, err := proto.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, want) {
				t.Fatalf("encoded input = %x, want %x", encoded, want)
			}
		})
	}
}

func TestConvertAppendInputToProtoHeaderIndependence(t *testing.T) {
	input := &AppendInput{Records: []AppendRecord{
		{Headers: []Header{{Name: []byte("first")}}},
		{},
		{Headers: []Header{{Name: []byte("second")}, {Name: []byte("third")}}},
	}}
	got := convertAppendInputToProto(input)
	got.Records[0].Headers[0].Name = []byte("changed")
	got.Records[0].Headers = append(got.Records[0].Headers, &pb.Header{Name: []byte("extra")})
	if got.Records[1].Headers != nil {
		t.Fatal("record without headers gained headers")
	}
	if len(got.Records[2].Headers) != 2 ||
		string(got.Records[2].Headers[0].Name) != "second" ||
		string(got.Records[2].Headers[1].Name) != "third" {
		t.Fatal("changing one record's headers changed another record")
	}
}
