package framing

import (
	"bytes"
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
