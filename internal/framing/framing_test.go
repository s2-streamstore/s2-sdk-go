package framing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseBasicFrame(t *testing.T) {
	p := NewS2SFrameParser()
	data := []byte("hello")
	frame := CreateFrame(data, false, CompressionNone)

	p.Push(frame)
	parsed, err := p.ParseFrame()

	if err != nil {
		t.Fatal(err)
	}
	if parsed.Terminal {
		t.Error("expected non-terminal")
	}
	body, err := parsed.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("got body %q, want %q", body, data)
	}
}

func TestParseTerminalFrame(t *testing.T) {
	p := NewS2SFrameParser()
	data := []byte("goodbye")
	frame := CreateFrame(data, true, CompressionNone)

	p.Push(frame)
	parsed, err := p.ParseFrame()

	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Terminal {
		t.Error("expected terminal")
	}
	if parsed.StatusCode == nil || *parsed.StatusCode != 0 {
		t.Errorf("expected status code 0, got %v", parsed.StatusCode)
	}
	body, err := parsed.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("got body %q, want %q", body, data)
	}
}

func TestParseTerminalFrameWithStatus(t *testing.T) {
	p := NewS2SFrameParser()
	data := []byte(`{"error":"not found"}`)
	frame := CreateFrameWithStatus(data, true, CompressionNone, 404)

	p.Push(frame)
	parsed, err := p.ParseFrame()

	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Terminal {
		t.Error("expected terminal")
	}
	if parsed.StatusCode == nil || *parsed.StatusCode != 404 {
		t.Errorf("expected status code 404, got %v", parsed.StatusCode)
	}
	body, err := parsed.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("got body %q, want %q", body, data)
	}
}

func TestParseCompression(t *testing.T) {
	// Data must be >= 1024 bytes for compression to be applied
	largeData := make([]byte, 2048)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	tests := []CompressionType{CompressionNone, CompressionZstd, CompressionGzip}

	for _, comp := range tests {
		p := NewS2SFrameParser()
		frame := CreateFrame(largeData, false, comp)

		p.Push(frame)
		parsed, err := p.ParseFrame()

		if err != nil {
			t.Fatal(err)
		}
		if parsed.Compression != comp {
			t.Errorf("got compression %v, want %v", parsed.Compression, comp)
		}
	}
}

func TestParseCompressionBelowThreshold(t *testing.T) {
	// Data below threshold should not be compressed even if compression is requested
	smallData := []byte("test")

	for _, comp := range []CompressionType{CompressionNone, CompressionZstd, CompressionGzip} {
		p := NewS2SFrameParser()
		frame := CreateFrame(smallData, false, comp)

		p.Push(frame)
		parsed, err := p.ParseFrame()

		if err != nil {
			t.Fatal(err)
		}
		if parsed.Compression != CompressionNone {
			t.Errorf("small data with comp=%v: got compression %v, want %v", comp, parsed.Compression, CompressionNone)
		}
	}
}

func TestParseIncremental(t *testing.T) {
	p := NewS2SFrameParser()
	data := []byte("test")
	frame := CreateFrame(data, false, CompressionNone)

	for i, b := range frame {
		p.Push([]byte{b})
		parsed, err := p.ParseFrame()

		if err != nil {
			t.Fatal(err)
		}

		if i < len(frame)-1 && parsed != nil {
			t.Error("got frame too early")
		}
		if i == len(frame)-1 && parsed == nil {
			t.Error("expected complete frame")
		}
	}
}

func TestParseMultiple(t *testing.T) {
	p := NewS2SFrameParser()

	data1 := []byte("one")
	data2 := []byte("two")

	frame1 := CreateFrame(data1, false, CompressionNone)
	frame2 := CreateFrame(data2, true, CompressionZstd)

	p.Push(append(frame1, frame2...))

	parsed1, err := p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	body1, err := parsed1.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body1, data1) {
		t.Errorf("frame1 body: got %q, want %q", body1, data1)
	}
	if parsed1.Terminal {
		t.Error("frame1 should not be terminal")
	}

	parsed2, err := p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	body2, err := parsed2.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body2, data2) {
		t.Errorf("frame2 body: got %q, want %q", body2, data2)
	}
	if !parsed2.Terminal {
		t.Error("frame2 should be terminal")
	}

	parsed3, err := p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	if parsed3 != nil {
		t.Error("expected no more frames")
	}
}

func TestParseEmpty(t *testing.T) {
	p := NewS2SFrameParser()
	frame := CreateFrame([]byte{}, false, CompressionNone)

	p.Push(frame)
	parsed, err := p.ParseFrame()

	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Body) != 0 {
		t.Errorf("got body length %d, want 0", len(parsed.Body))
	}
}

func TestParseLarge(t *testing.T) {
	p := NewS2SFrameParser()
	data := make([]byte, 8192)
	for i := range data {
		data[i] = byte(i)
	}

	frame := CreateFrame(data, false, CompressionNone)
	p.Push(frame)

	parsed, err := p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	body, err := parsed.DecompressedBody()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Error("large frame body mismatch")
	}
}

func TestParseOversized(t *testing.T) {
	p := NewS2SFrameParser()
	header := []byte{0xFF, 0xFF, 0xFF, 0x00}

	p.Push(header)
	_, err := p.ParseFrame()

	if err == nil {
		t.Error("expected error for oversized frame")
	}
}

func TestParsePartial(t *testing.T) {
	p := NewS2SFrameParser()

	p.Push([]byte{0x00, 0x00})
	parsed, err := p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	if parsed != nil {
		t.Error("expected nil with partial header")
	}

	p.Push([]byte{0x05, 0x00})
	parsed, err = p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	if parsed != nil {
		t.Error("expected nil with incomplete body")
	}

	p.Push([]byte{0x01, 0x02, 0x03, 0x04})
	parsed, err = p.ParseFrame()
	if err != nil {
		t.Fatal(err)
	}
	if parsed == nil {
		t.Error("expected complete frame")
	}
}

func TestCreateFrame(t *testing.T) {
	tests := []struct {
		data     []byte
		terminal bool
		comp     CompressionType
	}{
		{[]byte("hello"), false, CompressionNone},
		{[]byte("bye"), true, CompressionNone},
		{[]byte{}, false, CompressionNone},
	}

	for _, tt := range tests {
		frame := CreateFrame(tt.data, tt.terminal, tt.comp)

		if len(frame) < 4 {
			t.Fatal("frame too short")
		}

		length := int(frame[0])<<16 | int(frame[1])<<8 | int(frame[2])
		expectedLength := 1 + len(tt.data)
		if tt.terminal {
			expectedLength += 2
		}

		if length != expectedLength {
			t.Errorf("got length %d, want %d", length, expectedLength)
		}

		flag := frame[3]
		if terminal := (flag & 0x80) != 0; terminal != tt.terminal {
			t.Errorf("got terminal %v, want %v", terminal, tt.terminal)
		}

		if tt.terminal {
			if len(frame) < 6 {
				t.Fatal("terminal frame too short")
			}
			statusCode := int(frame[4])<<8 | int(frame[5])
			if statusCode != 0 {
				t.Errorf("got default status %d, want 0", statusCode)
			}
			if !bytes.Equal(frame[6:], tt.data) {
				t.Errorf("got data %v, want %v", frame[6:], tt.data)
			}
		} else {
			if !bytes.Equal(frame[4:], tt.data) {
				t.Errorf("got data %v, want %v", frame[4:], tt.data)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	tests := [][]byte{
		[]byte("hello"),
		[]byte(""),
		make([]byte, 1000),
		make([]byte, 2000),
	}

	for _, data := range tests {
		for _, terminal := range []bool{false, true} {
			for _, comp := range []CompressionType{CompressionNone, CompressionZstd, CompressionGzip} {
				frame := CreateFrame(data, terminal, comp)

				p := NewS2SFrameParser()
				p.Push(frame)

				parsed, err := p.ParseFrame()
				if err != nil {
					t.Fatal(err)
				}

				if parsed.Terminal != terminal {
					t.Errorf("terminal: got %v, want %v", parsed.Terminal, terminal)
				}
				body, err := parsed.DecompressedBody()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(body, data) {
					t.Errorf("body mismatch: got %d bytes, want %d bytes", len(body), len(data))
				}
			}
		}
	}
}

func TestAcceptEncoding(t *testing.T) {
	cases := []struct {
		name string
		c    CompressionType
		want string
	}{
		{"none", CompressionNone, ""},
		{"gzip", CompressionGzip, "gzip"},
		{"zstd", CompressionZstd, "zstd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.AcceptEncoding(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTerminalFrameIgnoresNonTerminalFlagBits(t *testing.T) {
	// Terminal frames must ignore compression bits (6-5) and reconnect-advised
	// bit (4) per s2s.md, even when bits 6-5 are 11 (normally invalid).
	body := []byte(`{"code":"bad_request","message":"oops"}`)
	frameLen := 1 + 2 + len(body) // flags + 2-byte status + body
	raw := make([]byte, 3+frameLen)
	raw[0] = byte(frameLen >> 16)
	raw[1] = byte(frameLen >> 8)
	raw[2] = byte(frameLen)
	// Terminal (0x80) | compression bits 11 (0x60) | reconnect advised (0x10)
	raw[3] = 0x80 | 0x60 | 0x10
	raw[4] = byte(400 >> 8)
	raw[5] = byte(400 & 0xFF)
	copy(raw[6:], body)

	frame, err := NewFrameReader(bytes.NewReader(raw)).ReadFrame()
	if err != nil {
		t.Fatalf("expected terminal frame with non-terminal bits set to parse, got: %v", err)
	}
	if !frame.Terminal {
		t.Fatal("expected Terminal == true")
	}
	if frame.Compression != CompressionNone {
		t.Fatalf("expected CompressionNone on terminal frame, got %v", frame.Compression)
	}
	if frame.ReconnectAdvised {
		t.Fatal("expected ReconnectAdvised == false on terminal frame")
	}
	if frame.StatusCode == nil || *frame.StatusCode != 400 {
		t.Fatalf("expected StatusCode 400, got %v", frame.StatusCode)
	}
	gotBody, err := frame.DecompressedBody()
	if err != nil {
		t.Fatalf("DecompressedBody failed: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body mismatch: got %q, want %q", gotBody, body)
	}
}

func TestFrameReaderReportsTruncatedFrameAsUnexpectedEOF(t *testing.T) {
	frame := CreateFrame([]byte("hello"), false, CompressionNone)
	fr := NewFrameReader(bytes.NewReader(append(frame, frame[:5]...)))
	if _, err := fr.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	if _, err := fr.ReadFrame(); err != io.ErrUnexpectedEOF {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestS2SFrameVectors(t *testing.T) {
	vectorPath := filepath.Join("..", "..", "s2-specs", "sdk", "vectors", "s2s-frames.json")
	raw, err := os.ReadFile(vectorPath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("s2-specs submodule not checked out")
	}
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}

	var doc struct {
		Cases []struct {
			Rule  string `json:"rule"`
			Name  string `json:"name"`
			Bytes []struct {
				Hex   string `json:"hex"`
				Zeros int    `json:"zeros"`
			} `json:"bytes"`
			Expect struct {
				Messages []struct {
					PayloadHex       string `json:"payload_hex"`
					PayloadLen       int    `json:"payload_len"`
					PayloadSHA256    string `json:"payload_sha256"`
					ReconnectAdvised bool   `json:"reconnect_advised"`
				} `json:"messages"`
				End struct {
					Type   string `json:"type"`
					Status int    `json:"status"`
				} `json:"end"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}

	for _, tc := range doc.Cases {
		t.Run(tc.Rule+"/"+tc.Name, func(t *testing.T) {
			var input bytes.Buffer
			for _, part := range tc.Bytes {
				if part.Hex != "" {
					b, err := hex.DecodeString(part.Hex)
					if err != nil {
						t.Fatalf("decode hex: %v", err)
					}
					input.Write(b)
				}
				if part.Zeros > 0 {
					input.Write(make([]byte, part.Zeros))
				}
			}

			fr := NewFrameReader(bytes.NewReader(input.Bytes()))
			type gotMsg struct {
				payload          []byte
				reconnectAdvised bool
			}
			var gotMsgs []gotMsg
			var endType string
			var endStatus int

			const (
				endClean          = "clean"
				endConnectionLost = "ConnectionLostError"
				endProtocolError  = "ProtocolError"
				endServerError    = "ServerError"
			)
			for {
				frame, err := fr.ReadFrame()
				if err != nil {
					switch {
					case errors.Is(err, io.EOF):
						endType = endClean
					case errors.Is(err, io.ErrUnexpectedEOF):
						endType = endConnectionLost
					case errors.Is(err, ErrMalformedFrame):
						endType = endProtocolError
					default:
						t.Fatalf("unexpected read error: %v", err)
					}
					break
				}
				body, err := frame.DecompressedBody()
				if err != nil {
					endType = endProtocolError
					break
				}
				if frame.Terminal {
					if frame.StatusCode == nil || *frame.StatusCode < 300 || *frame.StatusCode > 599 {
						endType = endProtocolError
					} else {
						endType = endServerError
						endStatus = *frame.StatusCode
					}
					break
				}
				gotMsgs = append(gotMsgs, gotMsg{payload: body, reconnectAdvised: frame.ReconnectAdvised})
			}

			if len(gotMsgs) != len(tc.Expect.Messages) {
				t.Fatalf("got %d messages, want %d (end=%s)", len(gotMsgs), len(tc.Expect.Messages), endType)
			}
			for i, want := range tc.Expect.Messages {
				got := gotMsgs[i]
				if got.reconnectAdvised != want.ReconnectAdvised {
					t.Errorf("msg %d: reconnect_advised=%v, want %v", i, got.reconnectAdvised, want.ReconnectAdvised)
				}
				if want.PayloadHex != "" {
					if gotHex := hex.EncodeToString(got.payload); gotHex != want.PayloadHex {
						t.Errorf("msg %d: payload hex=%s, want %s", i, gotHex, want.PayloadHex)
					}
				}
				if want.PayloadLen > 0 && len(got.payload) != want.PayloadLen {
					t.Errorf("msg %d: payload len=%d, want %d", i, len(got.payload), want.PayloadLen)
				}
				if want.PayloadSHA256 != "" {
					sum := sha256.Sum256(got.payload)
					if gotSum := hex.EncodeToString(sum[:]); gotSum != want.PayloadSHA256 {
						t.Errorf("msg %d: sha256=%s, want %s", i, gotSum, want.PayloadSHA256)
					}
				}
			}
			if endType != tc.Expect.End.Type {
				t.Fatalf("end type=%q, want %q", endType, tc.Expect.End.Type)
			}
			if tc.Expect.End.Type == endServerError && endStatus != tc.Expect.End.Status {
				t.Fatalf("end status=%d, want %d", endStatus, tc.Expect.End.Status)
			}
		})
	}
}
