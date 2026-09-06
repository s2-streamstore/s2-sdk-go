package framing

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestFrameEOFSemantics pins down the io.Reader-style contract that
// FrameReader.ReadFrame honors at end-of-stream:
//
//   - io.EOF is returned only when the stream ends exactly on a frame
//     boundary (truly empty, or immediately after a complete frame).
//   - io.ErrUnexpectedEOF is returned when the stream ends while a partial
//     (incomplete) frame is still buffered -- i.e. the S2S protocol was
//     truncated even though the underlying transport signaled a clean end.
//
// This mirrors io.ReadAll / bufio / json.Decoder, and it is what the read
// session and append-ack readers rely on to distinguish a graceful shutdown
// from a truncated one.
func TestFrameEOFSemantics(t *testing.T) {
	complete := CreateFrame([]byte("hello world payload"), false, CompressionNone)
	second := CreateFrame([]byte("second frame payload"), false, CompressionNone)

	cases := []struct {
		name            string
		input           []byte
		wantReadsBefore int // successful (frame, nil) reads before the trailing EOF read
		// Classification expected from the trailing EOF read:
		wantIsEOF      bool
		wantUnexpected bool
	}{
		{
			name:            "clean empty EOF",
			input:           nil,
			wantReadsBefore: 0,
			wantIsEOF:       true,
			wantUnexpected:  false,
		},
		{
			name:            "after complete frame, EOF",
			input:           complete,
			wantReadsBefore: 1,
			wantIsEOF:       true,
			wantUnexpected:  false,
		},
		{
			name:            "truncated mid-frame then EOF",
			input:           complete[:len(complete)-2],
			wantReadsBefore: 0,
			wantIsEOF:       false,
			wantUnexpected:  true,
		},
		{
			name:            "full frame + partial next then EOF",
			input:           append(append([]byte{}, complete...), second[:4]...),
			wantReadsBefore: 1,
			wantIsEOF:       false,
			wantUnexpected:  true,
		},
		{
			name:            "partial length header then EOF",
			input:           complete[:2],
			wantReadsBefore: 0,
			wantIsEOF:       false,
			wantUnexpected:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// bytes.Reader returns a clean (0, io.EOF) on exhaustion, matching
			// the shape http.Response.Body produces on a graceful HTTP
			// end-of-stream (HTTP/2 END_STREAM after a partial S2S frame).
			fr := NewFrameReader(bytes.NewReader(tc.input))

			for i := range tc.wantReadsBefore {
				frame, err := fr.ReadFrame()
				if err != nil {
					t.Fatalf("read #%d: unexpected error before EOF: %v", i+1, err)
				}
				if frame == nil {
					t.Fatalf("read #%d: expected a frame, got nil", i+1)
				}
			}

			frame, err := fr.ReadFrame()
			if frame != nil {
				t.Fatalf("expected nil frame at EOF, got %v", frame)
			}
			if gotIsEOF := errors.Is(err, io.EOF); gotIsEOF != tc.wantIsEOF {
				t.Errorf("errors.Is(err, io.EOF) = %v, want %v (err=%v)", gotIsEOF, tc.wantIsEOF, err)
			}
			if gotUnexpected := errors.Is(err, io.ErrUnexpectedEOF); gotUnexpected != tc.wantUnexpected {
				t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = %v, want %v (err=%v)", gotUnexpected, tc.wantUnexpected, err)
			}
			// io.EOF and io.ErrUnexpectedEOF are distinct, non-overlapping
			// sentinels: a truncation error must never also satisfy
			// errors.Is(err, io.EOF), and a clean EOF must never satisfy
			// errors.Is(err, io.ErrUnexpectedEOF). The two flags above already
			// encode that, but assert the cross-check explicitly to guard
			// against any future wrapping that collapses the two.
			if tc.wantUnexpected && errors.Is(err, io.EOF) {
				t.Errorf("truncation error must not satisfy errors.Is(err, io.EOF) (err=%v)", err)
			}
			if tc.wantIsEOF && errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("clean EOF must not satisfy errors.Is(err, io.ErrUnexpectedEOF) (err=%v)", err)
			}
		})
	}
}

// oneShotReader returns (n>0, io.EOF) in a single Read call -- the last data
// chunk and EOF arrive together, which the io.Reader contract explicitly
// permits. This exercises the n>0 branch of ReadFrame, which routes through
// pendingErr and must still translate the trailing io.EOF into
// io.ErrUnexpectedEOF when a partial frame remains buffered.
type oneShotReader struct {
	data []byte
}

func (r *oneShotReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.EOF
}

// TestFrameEOFOnSimultaneousDataAndEOF covers the n>0+io.EOF branch: when the
// last buffered bytes and io.EOF arrive in the same Read call, ReadFrame must
// still prefer a complete frame (happy path) and only report
// io.ErrUnexpectedEOF if the partial bytes do not form a complete frame.
func TestFrameEOFOnSimultaneousDataAndEOF(t *testing.T) {
	complete := CreateFrame([]byte("hello world payload"), false, CompressionNone)

	t.Run("complete frame with simultaneous EOF yields frame then clean EOF", func(t *testing.T) {
		fr := NewFrameReader(&oneShotReader{data: append([]byte{}, complete...)})

		frame, err := fr.ReadFrame()
		if err != nil || frame == nil {
			t.Fatalf("first read: expected frame, got frame=%v err=%v", frame, err)
		}
		body, derr := frame.DecompressedBody()
		if derr != nil {
			t.Fatal(derr)
		}
		if string(body) != "hello world payload" {
			t.Errorf("body = %q, want %q", body, "hello world payload")
		}

		// The reader is exhausted; the next read must report a clean EOF since
		// no partial frame remains buffered.
		if _, err := fr.ReadFrame(); !errors.Is(err, io.EOF) {
			t.Errorf("trailing read: errors.Is(err, io.EOF) = false, want true (err=%v)", err)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("trailing read: must not be ErrUnexpectedEOF after a complete frame (err=%v)", err)
		}
	})

	t.Run("partial frame with simultaneous EOF yields ErrUnexpectedEOF", func(t *testing.T) {
		fr := NewFrameReader(&oneShotReader{data: complete[:len(complete)-2]})

		_, err := fr.ReadFrame()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true (err=%v)", err)
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("errors.Is(err, io.EOF) = true, want false -- mid-frame truncation must not be reported as clean EOF (err=%v)", err)
		}
	})
}

// TestFrameEOFDoesNotSwallowOtherErrors verifies that non-EOF transport errors
// (e.g. a real io.ErrUnexpectedEOF surfaced by the HTTP layer on an abrupt
// connection drop) are forwarded unchanged -- the translation only applies to
// clean io.EOF, so abrupt-mode errors keep their existing classification.
func TestFrameEOFDoesNotSwallowOtherErrors(t *testing.T) {
	complete := CreateFrame([]byte("hello world payload"), false, CompressionNone)
	// Feed a complete frame followed by an abrupt io.ErrUnexpectedEOF (the
	// shape http.Response.Body.Read produces on a connection drop mid-frame).
	fr := NewFrameReader(&abruptAfterReader{
		data: append([]byte{}, complete...),
	})

	frame, err := fr.ReadFrame()
	if err != nil || frame == nil {
		t.Fatalf("first read: expected frame, got frame=%v err=%v", frame, err)
	}

	_, err = fr.ReadFrame()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true (err=%v)", err)
	}
	// Must be io.ErrUnexpectedEOF from the transport, not a clean io.EOF.
	if errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = true, want false -- abrupt close must not be reported as clean EOF (err=%v)", err)
	}
}

// abruptAfterReader yields data normally, then returns io.ErrUnexpectedEOF
// (rather than io.EOF) once exhausted, mirroring http.Response.Body.Read on
// an abrupt mid-stream connection close.
type abruptAfterReader struct {
	data []byte
}

func (r *abruptAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
