package framing

// S2S Protocol Frame Format:
//
// REGULAR MESSAGE:
//
//	┌─────────────┬────────────┬─────────────────────────────┐
//	│   LENGTH    │   FLAGS    │        PAYLOAD DATA         │
//	│  (3 bytes)  │  (1 byte)  │     (variable length)       │
//	├─────────────┼────────────┼─────────────────────────────┤
//	│ 0x00 00 XX  │ 0 CA XXXXX │  Compressed proto message   │
//	└─────────────┴────────────┴─────────────────────────────┘
//
// TERMINAL MESSAGE:
//
//	┌─────────────┬────────────┬─────────────┬───────────────┐
//	│   LENGTH    │   FLAGS    │ STATUS CODE │   JSON BODY   │
//	│  (3 bytes)  │  (1 byte)  │  (2 bytes)  │  (variable)   │
//	├─────────────┼────────────┼─────────────┼───────────────┤
//	│ 0x00 00 XX  │ 1 CA XXXXX │   HTTP Code │   JSON data   │
//	└─────────────┴────────────┴─────────────┴───────────────┘
//
// LENGTH = size of (FLAGS + PAYLOAD), does NOT include length header itself
// Protocol maximum: 2 MiB

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

const (
	// Maximum S2S frame size (2 MiB per protocol spec).
	MaxFrameSize = 2 * 1024 * 1024

	// Minimum spare buffer capacity offered to each network read.
	readChunkSize = 64 * 1024

	// Flag byte layout:
	//   ┌───┬───┬───┬───┬───┬───┬───┬───┐
	//   │ 7 │ 6 │ 5 │ 4 │ 3 │ 2 │ 1 │ 0 │
	//   ├───┼───┴───┼───┴───┴───┴───┴───┤
	//   │ T │  C C  │   Reserved (0s)   │
	//   └───┴───────┴───────────────────┘
	flagTerminal         = 0x80 // bit 7
	flagCompressionMask  = 0x60 // bits 6-5
	flagCompressionShift = 5
)

// S2SFrame represents a parsed S2S protocol frame.
type S2SFrame struct {
	Terminal    bool
	Compression CompressionType
	StatusCode  *int // Only for terminal frames
	Body        []byte
}

type S2SFrameParser struct {
	buffer []byte
	// off is the start of unconsumed data in buffer. Consumed frames advance
	// the offset instead of compacting the buffer; compaction happens lazily
	// in ensureSpare when more room is needed.
	off int
	// need is the total size of the pending incomplete frame (0 when
	// unknown), so readFrom can size the buffer for it in one allocation.
	need int
}

// NewS2SFrameParser creates a new frame parser.
func NewS2SFrameParser() *S2SFrameParser {
	return &S2SFrameParser{
		buffer: make([]byte, 0, 8192), // Start with 8KB capacity
	}
}

// ensureSpare guarantees at least n bytes of spare capacity past len(buffer),
// compacting consumed data first and growing only if that is not enough.
func (p *S2SFrameParser) ensureSpare(n int) {
	if cap(p.buffer)-len(p.buffer) >= n {
		return
	}

	unread := len(p.buffer) - p.off
	if p.off > 0 {
		copy(p.buffer, p.buffer[p.off:])
		p.buffer = p.buffer[:unread]
		p.off = 0
	}

	if cap(p.buffer)-len(p.buffer) >= n {
		return
	}

	// Grow: double size or accommodate new data
	newCap := cap(p.buffer) * 2
	if newCap < unread+n {
		newCap = unread + n
	}
	newBuffer := make([]byte, unread, newCap)
	copy(newBuffer, p.buffer)
	p.buffer = newBuffer
}

// Push adds data to the parser buffer.
func (p *S2SFrameParser) Push(data []byte) {
	p.ensureSpare(len(data))
	p.buffer = append(p.buffer, data...)
}

// readFrom reads once from r directly into the parser buffer's spare
// capacity, avoiding an intermediate chunk allocation and copy.
func (p *S2SFrameParser) readFrom(r io.Reader) (int, error) {
	spare := readChunkSize
	if p.need > 0 {
		// The pending frame's size is known: demand exactly the bytes that
		// complete it. Larger frames grow the buffer in one allocation;
		// nearly-complete ones avoid growing at all.
		if rem := p.need - (len(p.buffer) - p.off); rem > 0 {
			spare = rem
		}
	}
	p.ensureSpare(spare)
	n, err := r.Read(p.buffer[len(p.buffer):cap(p.buffer)])
	p.buffer = p.buffer[:len(p.buffer)+n]
	return n, err
}

// ParseFrame tries to parse the next frame from the buffer.
// Returns nil if not enough data is available.
func (p *S2SFrameParser) ParseFrame() (*S2SFrame, error) {
	buf := p.buffer[p.off:]

	// Need at least 4 bytes (3-byte length + 1-byte flag)
	if len(buf) < 4 {
		return nil, nil
	}

	// Read 3-byte length prefix (big-endian)
	length := uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])

	// Validate frame size
	if length == 0 {
		return nil, fmt.Errorf("invalid frame: length must be at least 1 (flag byte)")
	}
	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes (max %d)", length, MaxFrameSize)
	}

	// Check if we have the full message
	totalSize := 3 + int(length) // 3-byte prefix + message
	if len(buf) < totalSize {
		p.need = totalSize
		return nil, nil // Not enough data, need more
	}
	p.need = 0

	// Read flag byte
	flag := buf[3]
	terminal := (flag & flagTerminal) != 0

	// Parse compression from bits 6-5
	var compression CompressionType

	compressionBits := (flag & flagCompressionMask) >> flagCompressionShift
	switch compressionBits {
	case 0:
		compression = CompressionNone
	case 1:
		compression = CompressionZstd
	case 2:
		compression = CompressionGzip
	default:
		return nil, fmt.Errorf("unknown compression type: %d", compressionBits)
	}

	// Payload view into the buffer (length includes flag byte, so payload is
	// length - 1 bytes)
	payload := buf[4 : 4+int(length)-1]

	var statusCode *int

	// For terminal frames, extract status code (2 bytes)
	if terminal {
		if len(payload) < 2 {
			return nil, fmt.Errorf("terminal frame missing status code")
		}
		status := int(binary.BigEndian.Uint16(payload[0:2]))
		statusCode = &status
		payload = payload[2:]
	}

	// Single exactly-sized copy so the frame owns its body independently of
	// the parser buffer.
	body := make([]byte, len(payload))
	copy(body, payload)

	// Consume the frame by advancing the offset; compaction happens lazily
	// when more buffer space is needed.
	p.off += totalSize
	if p.off == len(p.buffer) {
		p.buffer = p.buffer[:0]
		p.off = 0
	}

	return &S2SFrame{
		Terminal:    terminal,
		Compression: compression,
		StatusCode:  statusCode,
		Body:        body,
	}, nil
}

// HasData returns true if the parser has buffered data.
func (p *S2SFrameParser) HasData() bool {
	return len(p.buffer) > p.off
}

// FrameReader provides frame reading from an io.Reader.
type FrameReader struct {
	reader     io.Reader
	parser     *S2SFrameParser
	pendingErr error
}

// NewFrameReader creates a new frame reader.
func NewFrameReader(reader io.Reader) *FrameReader {
	return &FrameReader{
		reader: reader,
		parser: NewS2SFrameParser(),
	}
}

// ReadFrame reads the next frame from the underlying reader.
func (fr *FrameReader) ReadFrame() (*S2SFrame, error) {
	for {
		// Try to parse a frame from current buffer
		frame, err := fr.parser.ParseFrame()
		if err != nil {
			return nil, err
		}
		if frame != nil {
			return frame, nil
		}

		if fr.pendingErr != nil {
			return nil, fr.pendingErr
		}

		n, err := fr.parser.readFrom(fr.reader)

		if err != nil {
			if n > 0 {
				fr.pendingErr = err

				continue
			}
			fr.pendingErr = err
			return nil, err
		}
	}
}

const (
	compressionThreshold = 1024
)

func CreateFrame(data []byte, terminal bool, compression CompressionType) []byte {
	return CreateFrameWithStatus(data, terminal, compression, 0)
}

func CreateFrameWithStatus(data []byte, terminal bool, compression CompressionType, statusCode int) []byte {
	compressedData := data
	actualCompression := CompressionNone

	if compression != CompressionNone && len(data) >= compressionThreshold {
		switch compression {
		case CompressionZstd:
			if compressed, err := compressZstd(data); err == nil {
				compressedData = compressed
				actualCompression = CompressionZstd
			}
		case CompressionGzip:
			if compressed, err := compressGzip(data); err == nil {
				compressedData = compressed
				actualCompression = CompressionGzip
			}
		}
	}

	var flag byte
	if terminal {
		flag |= flagTerminal
	}
	flag |= byte(actualCompression) << flagCompressionShift

	var payload []byte
	if terminal {
		payload = make([]byte, 1+2+len(compressedData))
		payload[0] = flag
		binary.BigEndian.PutUint16(payload[1:3], uint16(statusCode))
		copy(payload[3:], compressedData)
	} else {
		payload = make([]byte, 1+len(compressedData))
		payload[0] = flag
		copy(payload[1:], compressedData)
	}

	frame := make([]byte, 3+len(payload))
	frame[0] = byte(len(payload) >> 16)
	frame[1] = byte(len(payload) >> 8)
	frame[2] = byte(len(payload))
	copy(frame[3:], payload)

	return frame
}

// Shared encoder for EncodeAll, which is safe for concurrent use.
// Creating an encoder per call allocates its full window each time.
var zstdEncoder = func() *zstd.Encoder {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(err)
	}
	return encoder
}()

func compressZstd(data []byte) ([]byte, error) {
	return zstdEncoder.EncodeAll(data, nil), nil
}

func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Compress(data []byte, compression CompressionType) ([]byte, error) {
	switch compression {
	case CompressionNone:
		return data, nil
	case CompressionZstd:
		return compressZstd(data)
	case CompressionGzip:
		return compressGzip(data)
	default:
		return nil, fmt.Errorf("unknown compression type: %d", compression)
	}
}

func Decompress(data []byte, compression CompressionType) ([]byte, error) {
	switch compression {
	case CompressionNone:
		return data, nil
	case CompressionGzip:
		return gunzip(data)
	case CompressionZstd:
		return unzstd(data)
	default:
		return nil, fmt.Errorf("unknown compression type: %d", compression)
	}
}
