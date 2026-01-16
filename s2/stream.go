package s2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
)

type StreamClient struct {
	name        StreamName
	basinClient *BasinClient
	logger      *slog.Logger
}

func (b *BasinClient) Stream(name StreamName) *StreamClient {
	if name == "" {
		panic("stream name cannot be empty")
	}

	return &StreamClient{
		name:        name,
		basinClient: b,
		logger:      b.logger,
	}
}

func (s *StreamClient) Name() StreamName {
	return s.name
}

func (s *StreamClient) getHTTPClient() *http.Client {
	return s.basinClient.client.streamingClient
}

// Check the tail of the stream.
// Returns the next sequence number and timestamp to be assigned (tail).
func (s *StreamClient) CheckTail(ctx context.Context) (*TailResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	path := fmt.Sprintf("/streams/%s/records/tail", url.PathEscape(string(s.name)))

	return withRetries(ctx, s.basinClient.retryConfig, s.logger, func() (*TailResponse, error) {
		httpClient := &httpClient{
			client:      s.basinClient.httpClient,
			baseURL:     s.basinClient.baseURL,
			accessToken: s.basinClient.accessToken,
			logger:      s.logger,
			basinName:   s.basinClient.basinHeaderValue(),
			compression: s.basinClient.compression,
		}

		var result TailResponse
		if err := httpClient.request(ctx, "GET", path, nil, &result); err != nil {
			return nil, err
		}

		return &result, nil
	})
}

func parseHTTPError(resp *http.Response) error {
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return decodeAPIError(resp.StatusCode, body)
}

func parseReadBatch(data []byte) (*ReadBatch, error) {
	var pbBatch pb.ReadBatch
	if err := proto.Unmarshal(data, &pbBatch); err != nil {
		return nil, fmt.Errorf("failed to parse ReadBatch protobuf: %w", err)
	}
	return convertPbReadBatch(&pbBatch), nil
}

func convertPbReadBatch(pbBatch *pb.ReadBatch) *ReadBatch {
	records := make([]SequencedRecord, len(pbBatch.Records))
	for i, pbRecord := range pbBatch.Records {
		records[i] = convertPbSequencedRecord(pbRecord)
	}

	batch := &ReadBatch{
		Records: records,
	}

	if pbBatch.Tail != nil {
		batch.Tail = convertPbStreamPosition(pbBatch.Tail)
	}

	return batch
}

func convertPbSequencedRecord(pbRecord *pb.SequencedRecord) SequencedRecord {
	headers := make([]Header, len(pbRecord.Headers))
	for i, pbHeader := range pbRecord.Headers {
		headers[i] = Header{
			Name:  append([]byte(nil), pbHeader.Name...),
			Value: append([]byte(nil), pbHeader.Value...),
		}
	}

	bodyBytes := append([]byte(nil), pbRecord.Body...)

	return SequencedRecord{
		SeqNum:    pbRecord.SeqNum,
		Timestamp: pbRecord.Timestamp,
		Headers:   headers,
		Body:      bodyBytes,
	}
}

func convertPbStreamPosition(pbPos *pb.StreamPosition) *StreamPosition {
	if pbPos == nil {
		return nil
	}
	return &StreamPosition{
		SeqNum:    pbPos.SeqNum,
		Timestamp: pbPos.Timestamp,
	}
}

func isExpectedFrameReadError(err error) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, http.ErrBodyReadAfterClose):
		return true
	}

	// http2 package returns string errors for body closed
	errStr := err.Error()
	if strings.Contains(errStr, "response body closed") ||
		strings.Contains(errStr, "body closed") {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isExpectedFrameReadError(urlErr.Err)
	}

	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return isExpectedFrameReadError(netErr.Err)
	}

	return false
}

func convertPbAppendAck(pbAck *pb.AppendAck) *AppendAck {
	ack := &AppendAck{}
	if pbAck.Start != nil {
		ack.Start = StreamPosition{
			SeqNum:    pbAck.Start.SeqNum,
			Timestamp: pbAck.Start.Timestamp,
		}
	}
	if pbAck.End != nil {
		ack.End = StreamPosition{
			SeqNum:    pbAck.End.SeqNum,
			Timestamp: pbAck.End.Timestamp,
		}
	}
	if pbAck.Tail != nil {
		ack.Tail = StreamPosition{
			SeqNum:    pbAck.Tail.SeqNum,
			Timestamp: pbAck.Tail.Timestamp,
		}
	}
	return ack
}

const (
	// Maximum number of records allowed in a single append batch.
	MaxBatchRecords = 1000
	// Maximum metered size in bytes for a single append batch (1 MiB).
	MaxBatchMeteredBytes = 1 * 1024 * 1024 // 1 MiB
)

func prepareAppendInput(input *AppendInput) (*AppendInput, int64, error) {
	if input == nil {
		return nil, 0, fmt.Errorf("append input must not be nil")
	}
	if len(input.Records) == 0 {
		return nil, 0, fmt.Errorf("append input must contain at least one record")
	}
	if len(input.Records) > MaxBatchRecords {
		return nil, 0, fmt.Errorf("cannot append more than %d records at once, got %d", MaxBatchRecords, len(input.Records))
	}

	cloned := cloneAppendInput(input)
	size := MeteredBatchBytes(cloned.Records)
	if size > MaxBatchMeteredBytes {
		return nil, 0, fmt.Errorf("batch size %d bytes exceeds maximum %d bytes (1 MiB)", size, MaxBatchMeteredBytes)
	}
	return cloned, size, nil
}

func cloneAppendInput(input *AppendInput) *AppendInput {
	if input == nil {
		return nil
	}
	return &AppendInput{
		Records:      cloneAppendRecords(input.Records),
		MatchSeqNum:  cloneUint64Ptr(input.MatchSeqNum),
		FencingToken: cloneStringPtr(input.FencingToken),
	}
}

func cloneAppendRecords(records []AppendRecord) []AppendRecord {
	cloned := make([]AppendRecord, len(records))
	for i, record := range records {
		if len(record.Headers) > 0 {
			cloned[i].Headers = make([]Header, len(record.Headers))
			for j, h := range record.Headers {
				cloned[i].Headers[j] = Header{
					Name:  append([]byte(nil), h.Name...),
					Value: append([]byte(nil), h.Value...),
				}
			}
		}

		if record.Timestamp != nil {
			ts := *record.Timestamp
			cloned[i].Timestamp = &ts
		}

		if len(record.Body) > 0 {
			buf := make([]byte, len(record.Body))
			copy(buf, record.Body)
			cloned[i].Body = buf
		}
	}
	return cloned
}

func cloneUint64Ptr(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	val := *src
	return &val
}

func cloneStringPtr(src *string) *string {
	if src == nil {
		return nil
	}
	val := *src
	return &val
}

func cloneReadSessionOptions(opts *ReadOptions) *ReadOptions {
	if opts == nil {
		return nil
	}

	clone := *opts

	if opts.SeqNum != nil {
		val := *opts.SeqNum
		clone.SeqNum = &val
	}
	if opts.Timestamp != nil {
		val := *opts.Timestamp
		clone.Timestamp = &val
	}
	if opts.TailOffset != nil {
		val := *opts.TailOffset
		clone.TailOffset = &val
	}
	if opts.Count != nil {
		val := *opts.Count
		clone.Count = &val
	}
	if opts.Bytes != nil {
		val := *opts.Bytes
		clone.Bytes = &val
	}
	if opts.Wait != nil {
		val := *opts.Wait
		clone.Wait = &val
	}
	if opts.Until != nil {
		val := *opts.Until
		clone.Until = &val
	}
	if opts.Clamp != nil {
		val := *opts.Clamp
		clone.Clamp = &val
	}

	return &clone
}

func isRetryableReadError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil || errors.Is(err, errSessionClosed) {
		return false
	}

	var s2Err *S2Error
	if errors.As(err, &s2Err) {
		return s2Err.IsRetryable()
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return true
	}

	var goAwayErr http2.GoAwayError
	if errors.As(err, &goAwayErr) {
		return true
	}

	if strings.Contains(strings.ToLower(err.Error()), "http2: client connection lost") {
		return true
	}

	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}

	if errors.Is(err, http.ErrBodyReadAfterClose) {
		return true
	}

	return false
}

func isStreamResetError(err error) bool {
	var se http2.StreamError
	return errors.As(err, &se)
}

func shouldRetryError(err error, config *RetryConfig, input *AppendInput) bool {
	s2Err, ok := err.(*S2Error)
	if !ok {
		return false
	}

	if !s2Err.IsRetryable() {
		return false
	}

	if config.AppendRetryPolicy == AppendRetryPolicyNoSideEffects {
		if input != nil && input.MatchSeqNum == nil {
			return false
		}
	}

	return true
}
