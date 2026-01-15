package s2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

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
)

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
		client:      s.getHTTPClient(),
		baseURL:     s.basinClient.baseURL,
		accessToken: s.basinClient.accessToken,
		logger:      s.logger,
		basinName:   s.basinClient.basinHeaderValue(),
		compression: s.basinClient.compression,
	}

	ack, err := withAppendRetries(ctx, s.basinClient.retryConfig, s.logger, prepared, func() (*AppendAck, error) {
		var pbAck pb.AppendAck
		if err := httpClient.requestProto(ctx, "POST", path, pbInput, &pbAck); err != nil {
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
	streamClient  *StreamClient
	acksCh        chan *AppendAck
	errorsCh      chan error
	closed        chan struct{}
	closeOnce     sync.Once
	conn          *http.Response
	requestWriter io.WriteCloser
}

func (s *StreamClient) createAppendSession(ctx context.Context) (*transportAppendSession, error) {
	logDebug(s.logger, "transport: creating append session",
		"stream", string(s.name))

	session := &transportAppendSession{
		streamClient: s,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, pipeReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.streamClient.basinClient.accessToken)
	req.Header.Set("Accept", "application/protobuf")
	req.Header.Set("Accept-Encoding", "zstd, gzip")
	req.Header.Set("Content-Type", "s2s/proto")
	if basinName := p.streamClient.basinClient.basinHeaderValue(); basinName != "" {
		req.Header.Set("s2-basin", basinName)
	}

	resp, err := p.streamClient.getHTTPClient().Do(req)
	if err != nil {
		pipeWriter.Close()

		if isStreamResetError(err) {
			return makeStreamResetError(err, "pipelined session")
		}

		logError(p.streamClient.logger, "pipelined append session connect error",
			"stream", string(p.streamClient.name),
			"error", err)

		return fmt.Errorf("failed to connect: %w", err)
	}

	if resp.StatusCode >= 400 {
		pipeWriter.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		return decodeAPIError(resp.StatusCode, body)
	}

	p.conn = resp
	p.requestWriter = pipeWriter

	go p.readAcksLoop()

	return nil
}

func (p *transportAppendSession) appendInput(input *AppendInput) error {
	select {
	case <-p.closed:
		return ErrSessionClosed
	default:
	}

	pbInput := convertAppendInputToProto(input)

	data, err := proto.Marshal(pbInput)
	if err != nil {
		return fmt.Errorf("failed to marshal append input: %w", err)
	}

	frame := framing.CreateFrame(data, false, CompressionNone)

	if _, err := p.requestWriter.Write(frame); err != nil {
		return fmt.Errorf("failed to write frame: %w", err)
	}

	return nil
}

func (p *transportAppendSession) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)

		if p.requestWriter != nil {
			p.requestWriter.Close()
		}
		if p.conn != nil {
			p.conn.Body.Close()
		}
	})
	return nil
}

func (p *transportAppendSession) readAcksLoop() {
	defer p.Close()
	defer close(p.acksCh)
	defer close(p.errorsCh)

	frameReader := framing.NewFrameReader(p.conn.Body)

	for {
		frame, err := frameReader.ReadFrame()
		if err != nil {
			if isExpectedFrameReadError(err) {
				return
			}
			select {
			case <-p.closed:
				return
			case p.errorsCh <- fmt.Errorf("read frame error: %w", err):
			}
			return
		}

		if err := p.handleFrame(frame); err != nil {
			select {
			case p.errorsCh <- err:
			case <-p.closed:
			}
			return
		}
	}
}

func (p *transportAppendSession) handleFrame(frame *framing.S2SFrame) error {
	body, err := frame.DecompressedBody()
	if err != nil {
		return fmt.Errorf("failed to decompress frame: %w", err)
	}

	if frame.Terminal {
		if frame.StatusCode != nil && *frame.StatusCode >= 400 {
			return decodeAPIError(*frame.StatusCode, body)
		}
		return nil
	}

	var pbAck pb.AppendAck
	if err := proto.Unmarshal(body, &pbAck); err != nil {
		return fmt.Errorf("failed to parse AppendAck: %w", err)
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

	pbRecords := make([]*pb.AppendRecord, len(input.Records))
	for i, record := range input.Records {
		pbRecord := &pb.AppendRecord{
			Timestamp: record.Timestamp,
		}

		if len(record.Headers) > 0 {
			pbRecord.Headers = make([]*pb.Header, len(record.Headers))
			for j, header := range record.Headers {
				pbRecord.Headers[j] = &pb.Header{
					Name:  header.Name,
					Value: header.Value,
				}
			}
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
