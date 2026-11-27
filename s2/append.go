package s2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	framing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

// Policy for retrying append operations.
type AppendRetryPolicy string

const (
	// Retry all append operations, including those that may have side effects (default).
	AppendRetryPolicyAll AppendRetryPolicy = "all"
	// Only retry append operations that are guaranteed to have no side effects.
	AppendRetryPolicyNoSideEffects AppendRetryPolicy = "noSideEffects"

	// The spec's spelling. Treated as AppendRetryPolicyNoSideEffects, so a
	// policy read from configuration never silently falls back to all.
	appendRetryPolicyNoSideEffectsSpec AppendRetryPolicy = "no_side_effects"
)

func (p AppendRetryPolicy) isNoSideEffects() bool {
	return p == AppendRetryPolicyNoSideEffects || p == appendRetryPolicyNoSideEffectsSpec
}

// Appends one or more records to the stream.
// All records in a single append call must use the same format (either all string or all bytes).
// For high-throughput sequential appends, use [AppendSession] instead.
func (s *StreamClient) Append(ctx context.Context, input *AppendInput) (*AppendAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	prepared, _, err := prepareAppendInput(input)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/streams/%s/records", url.PathEscape(string(s.name)))
	pbInput := convertAppendInputToProto(prepared)

	httpClient := &httpClient{
		client:      s.basinClient.httpClient,
		baseURL:     s.basinClient.baseURL,
		accessToken: s.basinClient.accessToken,
		logger:      s.logger,
		basinName:   s.basinClient.basinHeaderValue(),
		compression: s.basinClient.compression,
	}

	ack, err := withAppendRetries(ctx, s.basinClient.retryConfig, s.logger, prepared, func(ctx context.Context) (*AppendAck, error) {
		var pbAck pb.AppendAck
		if err := httpClient.requestProto(
			ctx,
			"POST",
			path,
			pbInput,
			&pbAck,
			s.encryptionKey,
			prepared.StreamConfig,
		); err != nil {
			return nil, err
		}

		return convertPbAppendAck(&pbAck), nil
	})

	if err != nil {
		return nil, err
	}

	return ack, nil
}

type transportAppendSession struct {
	streamClient      *StreamClient
	streamConfig      *StreamConfig
	acksCh            chan *AppendAck
	errorsCh          chan error
	closed            chan struct{}
	closeOnce         sync.Once
	mu                sync.Mutex
	conn              *http.Response
	requestWriter     io.WriteCloser
	pendingWrites     int
	terminalErr       error
	reconnectAdvised  atomic.Bool
	reconnectDeclined atomic.Bool
	ackReceived       atomic.Bool
	poisonCapture     *poisonCapture
	// halfClosed defers closing the request to the in-progress write, if
	// any, so a frame is never cut short.
	halfClosed bool
	writing    bool
	// completedElsewhere counts batches sent on this session that were
	// acknowledged on another session after a retry (protected by AppendSession.inflightMu).
	completedElsewhere int
}

// errAppendHalfClosed reports that an input was not sent because the request
// was half-closed for a reconnect.
var errAppendHalfClosed = errors.New("append request half-closed")

func (s *StreamClient) createAppendSession(ctx context.Context, streamConfig *StreamConfig) (*transportAppendSession, error) {
	if s.appendSessionFactory != nil {
		return s.appendSessionFactory(ctx)
	}
	logDebug(s.logger, "transport: creating append session",
		"stream", string(s.name))

	session := &transportAppendSession{
		streamClient: s,
		streamConfig: streamConfig,
		acksCh:       make(chan *AppendAck, appendAckChannelBuffer),
		errorsCh:     make(chan error, 1),
		closed:       make(chan struct{}),
	}

	if err := session.start(ctx); err != nil {
		logDebug(s.logger, "transport: append session start failed",
			"stream", string(s.name),
			"error", err)
		return nil, err
	}

	logDebug(s.logger, "transport: append session started",
		"stream", string(s.name))

	return session, nil
}

func (p *transportAppendSession) start(ctx context.Context) error {
	path := fmt.Sprintf("/streams/%s/records", url.PathEscape(string(p.streamClient.name)))
	reqURL := p.streamClient.basinClient.baseURL + path

	pipeReader, pipeWriter := io.Pipe()

	ctx, capture := capturePoisonHandle(ctx)
	p.poisonCapture = capture

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, pipeReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.streamClient.basinClient.accessToken)
	req.Header.Set("Accept", "application/protobuf")
	if encoding := p.streamClient.basinClient.compression.AcceptEncoding(); encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	req.Header.Set("Content-Type", "s2s/proto")
	if basinName := p.streamClient.basinClient.basinHeaderValue(); basinName != "" {
		req.Header.Set("s2-basin", basinName)
	}
	setEncryptionKeyHeader(req.Header, p.streamClient.encryptionKey)
	if err := setStreamConfigHeader(req.Header, p.streamConfig); err != nil {
		return err
	}

	p.mu.Lock()
	p.requestWriter = pipeWriter
	p.mu.Unlock()

	go p.connectAndRead(req, pipeWriter)

	return nil
}

func (p *transportAppendSession) connectAndRead(req *http.Request, pipeWriter *io.PipeWriter) {
	resp, err := p.streamClient.getHTTPClient().Do(req)
	if err != nil {
		pipeWriter.CloseWithError(err)

		if isStreamResetError(err) {
			p.reportError(makeStreamResetError(err, "pipelined session"))
			return
		}

		logError(p.streamClient.logger, "pipelined append session connect error",
			"stream", string(p.streamClient.name),
			"error", err)
		p.reportError(fmt.Errorf("failed to connect: %w", err))
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseHTTPError(resp)
		if shouldRetireConnection(apiErr) {
			p.poisonCapture.poison()
		}
		pipeWriter.CloseWithError(apiErr)
		p.reportError(apiErr)
		return
	}

	p.mu.Lock()
	select {
	case <-p.closed:
		p.mu.Unlock()
		resp.Body.Close()
		return
	default:
	}
	p.conn = resp
	p.mu.Unlock()

	go p.readAcksLoop(resp)
}

func (p *transportAppendSession) reportError(err error) {
	if err == nil {
		return
	}
	p.recordTerminalError(err)
	select {
	case <-p.closed:
	case p.errorsCh <- err:
	default:
	}
}

func (p *transportAppendSession) reportErrorIfOpen(err error) {
	select {
	case <-p.closed:
		return
	default:
		p.reportError(err)
	}
}

func (p *transportAppendSession) recordTerminalError(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if p.terminalErr == nil {
		p.terminalErr = err
	}
	p.mu.Unlock()
}

func (p *transportAppendSession) terminalError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminalErr
}

func (p *transportAppendSession) marshalInput(input *AppendInput) ([]byte, error) {
	pbInput := convertAppendInputToProto(input)

	frame, err := framing.MarshalProtoFrame(pbInput, p.streamClient.basinClient.compression)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal append input: %w", err)
	}
	return frame, nil
}

// beginWrite reserves the request writer for a single frame write. Callers
// must pair a successful beginWrite with writeFrame.
func (p *transportAppendSession) beginWrite() (io.WriteCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A handover may close the request right after half-closing it; the
	// input was still never written, so report it as refused, not failed.
	if p.halfClosed {
		return nil, errAppendHalfClosed
	}
	if p.isClosed() || p.requestWriter == nil {
		if p.terminalErr != nil {
			return nil, p.terminalErr
		}
		return nil, ErrSessionClosed
	}
	p.writing = true
	return p.requestWriter, nil
}

func (p *transportAppendSession) writeFrame(writer io.WriteCloser, frame []byte) error {
	p.markWriteSignalled()
	_, err := writer.Write(frame)
	p.finishWrite()
	if err != nil {
		if terminalErr := p.terminalError(); terminalErr != nil {
			return terminalErr
		}
		if p.isClosed() {
			return ErrSessionClosed
		}
		return fmt.Errorf("failed to write frame: %w", err)
	}

	return nil
}

func (p *transportAppendSession) appendInput(input *AppendInput) error {
	frame, err := p.marshalInput(input)
	if err != nil {
		return err
	}
	writer, err := p.beginWrite()
	if err != nil {
		return err
	}
	return p.writeFrame(writer, frame)
}

func (p *transportAppendSession) ReconnectAdvised() bool {
	return p.reconnectAdvised.Load()
}

func (p *transportAppendSession) holdInputsForReconnect() bool {
	return p.ReconnectAdvised() && !p.reconnectDeclined.Load()
}

func (p *transportAppendSession) declineReconnect() {
	p.reconnectDeclined.Store(true)
}

// halfClose ends input while acknowledgements remain readable.
func (p *transportAppendSession) halfClose() {
	p.mu.Lock()
	p.halfClosed = true
	var requestWriter io.WriteCloser
	if !p.writing {
		requestWriter, p.requestWriter = p.requestWriter, nil
	}
	p.mu.Unlock()

	if requestWriter != nil {
		requestWriter.Close()
	}
}

func (p *transportAppendSession) finishWrite() {
	p.mu.Lock()
	p.writing = false
	var requestWriter io.WriteCloser
	if p.halfClosed {
		requestWriter, p.requestWriter = p.requestWriter, nil
	}
	p.mu.Unlock()

	if requestWriter != nil {
		requestWriter.Close()
	}
}

func (p *transportAppendSession) markWriteSignalled() {
	p.mu.Lock()
	p.pendingWrites++
	p.mu.Unlock()
}

func (p *transportAppendSession) markAckReceived() {
	p.ackReceived.Store(true)
	p.mu.Lock()
	if p.pendingWrites > 0 {
		p.pendingWrites--
	}
	p.mu.Unlock()
}

func (p *transportAppendSession) effectSignalled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pendingWrites > 0
}

func (p *transportAppendSession) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)

		p.mu.Lock()
		requestWriter := p.requestWriter
		p.requestWriter = nil
		conn := p.conn
		p.conn = nil
		p.mu.Unlock()

		if requestWriter != nil {
			requestWriter.Close()
		}
		if conn != nil {
			conn.Body.Close()
		}
	})
	return nil
}

func (p *transportAppendSession) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func (p *transportAppendSession) readAcksLoop(conn *http.Response) {
	defer conn.Body.Close()
	defer p.Close()
	defer close(p.acksCh)
	defer close(p.errorsCh)

	frameReader := framing.NewFrameReader(conn.Body)

	for {
		frame, err := frameReader.ReadFrame()
		if err != nil {
			// A response cut off mid-frame is a lost connection (spec T9).
			// Record it before closing so blocked writes report it too.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				p.reportErrorIfOpen(makeConnectionLostError(err, "append session"))
				return
			}
			if isExpectedFrameReadError(err) {
				return
			}
			if errors.Is(err, framing.ErrMalformedFrame) {
				p.reportErrorIfOpen(makeProtocolError(err, "append session frame"))
				return
			}
			p.reportErrorIfOpen(fmt.Errorf("read frame error: %w", err))
			return
		}

		if err := p.handleFrame(frame); err != nil {
			p.reportErrorIfOpen(err)
			return
		}
	}
}

func (p *transportAppendSession) handleFrame(frame *framing.S2SFrame) error {
	body, err := frame.DecompressedBody()
	if err != nil {
		return makeProtocolError(err, "append session frame decompression")
	}

	if frame.Terminal {
		if frame.StatusCode == nil || *frame.StatusCode < 300 || *frame.StatusCode > 599 {
			return makeProtocolError(
				fmt.Errorf("terminal frame status out of range: %s", formatTerminalStatus(frame.StatusCode)),
				"append session terminal frame",
			)
		}
		err := decodeAPIError(*frame.StatusCode, body)
		if shouldRetireConnection(err) {
			p.poisonCapture.poison()
		}
		return err
	}

	if frame.ReconnectAdvised && !p.reconnectAdvised.Swap(true) {
		p.poisonCapture.poison()
	}

	var pbAck pb.AppendAck
	if err := proto.Unmarshal(body, &pbAck); err != nil {
		return makeProtocolError(err, "append session ack decode")
	}

	ack := convertPbAppendAck(&pbAck)

	select {
	case p.acksCh <- ack:
	case <-p.closed:
	}

	return nil
}

func convertAppendInputToProto(input *AppendInput) *pb.AppendInput {
	if input == nil {
		return nil
	}

	headerCount := 0
	for _, record := range input.Records {
		headerCount += len(record.Headers)
	}

	recordValues := make([]pb.AppendRecord, len(input.Records))
	pbRecords := make([]*pb.AppendRecord, len(input.Records))
	headerValues := make([]pb.Header, headerCount)
	headerPtrs := make([]*pb.Header, headerCount)
	headerOffset := 0
	for i, record := range input.Records {
		pbRecord := &recordValues[i]
		pbRecord.Timestamp = record.Timestamp

		if len(record.Headers) > 0 {
			end := headerOffset + len(record.Headers)
			pbRecord.Headers = headerPtrs[headerOffset:end:end]
			for j, header := range record.Headers {
				pbHeader := &headerValues[headerOffset+j]
				pbHeader.Name = header.Name
				pbHeader.Value = header.Value
				pbRecord.Headers[j] = pbHeader
			}
			headerOffset = end
		}

		if len(record.Body) > 0 {
			pbRecord.Body = record.Body
		}

		pbRecords[i] = pbRecord
	}

	return &pb.AppendInput{
		Records:      pbRecords,
		MatchSeqNum:  input.MatchSeqNum,
		FencingToken: input.FencingToken,
	}
}
