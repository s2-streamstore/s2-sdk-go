package s2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

const (
	tailWatchdogTimeout      = 20 * time.Second
	readSessionRecordsBuffer = 100
)

var errSessionClosed = errors.New("read session closed")

type ReadOptions struct {
	// Start from a sequence number.
	SeqNum *uint64 `json:"seq_num,omitempty"`
	// Start from a timestamp.
	Timestamp *uint64 `json:"timestamp,omitempty"`
	// Start from number of records before the next sequence number.
	TailOffset *int64 `json:"tail_offset,omitempty"`
	// Record count limit.
	// Non-streaming reads are capped by the default limit of 1000 records.
	Count *uint64 `json:"count,omitempty"`
	// Metered bytes limit.
	// Non-streaming reads are capped by the default limit of 1 MiB.
	Bytes *uint64 `json:"bytes,omitempty"`
	// Duration in seconds to wait for new records.
	// The default duration is 0 if there is a bound on `count`, `bytes`, or `until`, and otherwise infinite.
	// Non-streaming reads are always bounded on `count` and `bytes`, so you can achieve long poll semantics by specifying a non-zero duration up to 60 seconds.
	// In the context of an SSE or S2S streaming read, the duration will bound how much time can elapse between records throughout the lifetime of the session.
	Wait *int32 `json:"wait,omitempty"`
	// Exclusive timestamp to read until.
	Until *uint64 `json:"until,omitempty"`
	// Start reading from the tail if the requested position is beyond it.
	// Otherwise, a `416 Range Not Satisfiable` response is returned.
	Clamp *bool `json:"clamp,omitempty"`
}

func buildReadQueryParams(opts *ReadOptions) string {
	if opts == nil {
		return ""
	}
	params := url.Values{}
	if opts.SeqNum != nil {
		params.Set("seq_num", strconv.FormatUint(*opts.SeqNum, 10))
	}
	if opts.Timestamp != nil {
		params.Set("timestamp", strconv.FormatUint(*opts.Timestamp, 10))
	}
	if opts.TailOffset != nil {
		params.Set("tail_offset", strconv.FormatInt(*opts.TailOffset, 10))
	}
	if opts.Count != nil {
		params.Set("count", strconv.FormatUint(*opts.Count, 10))
	}
	if opts.Bytes != nil {
		params.Set("bytes", strconv.FormatUint(*opts.Bytes, 10))
	}
	if opts.Wait != nil {
		params.Set("wait", strconv.FormatInt(int64(*opts.Wait), 10))
	}
	if opts.Until != nil {
		params.Set("until", strconv.FormatUint(*opts.Until, 10))
	}
	if opts.Clamp != nil {
		params.Set("clamp", strconv.FormatBool(*opts.Clamp))
	}
	return params.Encode()
}

func (s *StreamClient) Read(ctx context.Context, opts *ReadOptions) (*ReadBatch, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	queryParams := buildReadQueryParams(opts)
	path := fmt.Sprintf("/streams/%s/records", url.PathEscape(string(s.name)))
	if len(queryParams) > 0 {
		path += "?" + queryParams
	}

	batch, err := withRetries(ctx, s.basinClient.retryConfig, s.logger, func() (*ReadBatch, error) {
		httpClient := &httpClient{
			client:      s.getHTTPClient(),
			baseURL:     s.basinClient.baseURL,
			accessToken: s.basinClient.accessToken,
			logger:      s.logger,
			basinName:   s.basinClient.basinHeaderValue(),
			compression: s.basinClient.compression,
		}

		var pbBatch pb.ReadBatch
		if err := httpClient.requestProto(ctx, "GET", path, nil, &pbBatch); err != nil {
			return nil, err
		}

		return convertPbReadBatch(&pbBatch), nil
	})

	if err != nil {
		return nil, fmt.Errorf("unary read failed: %w", err)
	}

	return batch, nil
}

type streamReader struct {
	streamClient *StreamClient

	recordsCh chan SequencedRecord
	errorCh   chan error
	closed    chan struct{}
	closeOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc

	baseOpts *ReadOptions

	stateMu    sync.RWMutex
	lastTail   *StreamPosition
	nextSeq    uint64
	nextTS     uint64
	hasNextSeq bool

	respBody       io.ReadCloser
	bodyMu         sync.Mutex
	recordsRead    uint64
	bytesRead      uint64
	startTime      time.Time
	lastRecordTime time.Time
	retryConfig    *RetryConfig
	logger         *slog.Logger
}

func (s *StreamClient) newStreamReader(ctx context.Context, opts *ReadOptions) (*streamReader, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var retryCfg *RetryConfig
	if s.basinClient.retryConfig != nil {
		cfgCopy := *s.basinClient.retryConfig
		retryCfg = &cfgCopy
	} else if DefaultRetryConfig != nil {
		cfgCopy := *DefaultRetryConfig
		retryCfg = &cfgCopy
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)

	now := time.Now()
	session := &streamReader{
		streamClient:   s,
		recordsCh:      make(chan SequencedRecord, readSessionRecordsBuffer),
		errorCh:        make(chan error, 1),
		closed:         make(chan struct{}),
		ctx:            sessionCtx,
		cancel:         sessionCancel,
		baseOpts:       cloneReadSessionOptions(opts),
		startTime:      now,
		lastRecordTime: now,
		retryConfig:    retryCfg,
		logger:         s.basinClient.logger,
	}

	go session.run()

	return session, nil
}

func (r *streamReader) run() {
	defer close(r.recordsCh)
	defer close(r.errorCh)

	logInfo(r.logger, "s2 read session start", "stream", string(r.streamClient.name))

	cfg := DefaultRetryConfig
	if r.retryConfig != nil {
		cfg = r.retryConfig
	}

	maxAttempts := cfg.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	opts := r.buildAttemptOptions(0)
	consecutiveFailures := 0

	for {
		if r.limitsReached() {
			logInfo(r.logger, "s2 read session limits reached")
			return
		}

		logInfo(r.logger, "s2 read session attempt", "stream", string(r.streamClient.name), "attempt", consecutiveFailures+1)

		recordsBefore := r.recordsRead

		tailCtx, tailCancel := context.WithCancel(r.ctx)
		err := r.runOnce(tailCtx, opts)
		tailCancel()

		if err == nil || errors.Is(err, errSessionClosed) {
			return
		}
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return
		}

		madeProgress := r.recordsRead > recordsBefore
		if madeProgress {
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
		}

		if !isRetryableReadError(r.ctx, err) || consecutiveFailures >= maxAttempts {
			if r.ctx.Err() == nil && !errors.Is(err, errSessionClosed) {
				if consecutiveFailures >= maxAttempts {
					logError(r.logger, "s2 read session max attempts exhausted",
						"stream", string(r.streamClient.name),
						"attempts", consecutiveFailures,
						"error", err)
				} else {
					logError(r.logger, "s2 read session non-retryable error",
						"stream", string(r.streamClient.name),
						"error", err)
				}
				r.sendError(err)
			}
			return
		}

		delay := calculateRetryBackoff(cfg, consecutiveFailures)
		opts = r.buildAttemptOptions(delay)

		logInfo(r.logger, "s2 read session retrying",
			"stream", string(r.streamClient.name),
			"attempt", consecutiveFailures,
			"max_attempts", maxAttempts,
			"delay", delay,
			"error", err)

		select {
		case <-r.ctx.Done():
			return
		case <-r.closed:
			return
		default:
		}

		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func (r *streamReader) Records() <-chan SequencedRecord {
	return r.recordsCh
}

func (r *streamReader) Errors() <-chan error {
	return r.errorCh
}

func (r *streamReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.closed)
		r.cancel()
	})
	return nil
}

func (r *streamReader) NextReadPosition() *StreamPosition {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	if !r.hasNextSeq {
		return nil
	}
	return &StreamPosition{
		SeqNum:    r.nextSeq,
		Timestamp: r.nextTS,
	}
}

func (r *streamReader) LastObservedTail() *StreamPosition {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	if r.lastTail == nil {
		return nil
	}
	copy := *r.lastTail
	return &copy
}

func (r *streamReader) closeRespBody() {
	r.bodyMu.Lock()
	body := r.respBody
	r.respBody = nil
	r.bodyMu.Unlock()
	if body != nil {
		body.Close()
	}
}

func (r *streamReader) buildAttemptOptions(plannedDelay time.Duration) *ReadOptions {
	var opts *ReadOptions
	if r.baseOpts != nil {
		opts = cloneReadSessionOptions(r.baseOpts)
	} else if r.hasNextSeq {
		opts = &ReadOptions{}
	} else {
		return nil
	}

	if r.hasNextSeq {
		seq := r.nextSeq
		opts.SeqNum = &seq
		opts.Timestamp = nil
		opts.TailOffset = nil
	}

	if r.baseOpts != nil {
		if r.baseOpts.Count != nil {
			baseCount := *r.baseOpts.Count
			var remaining uint64
			if r.recordsRead >= baseCount {
				remaining = 0
			} else {
				remaining = baseCount - r.recordsRead
			}
			opts.Count = Uint64(remaining)
		}
		if r.baseOpts.Bytes != nil {
			baseBytes := *r.baseOpts.Bytes
			var remaining uint64
			if r.bytesRead >= baseBytes {
				remaining = 0
			} else {
				remaining = baseBytes - r.bytesRead
			}
			opts.Bytes = Uint64(remaining)
		}
		if r.baseOpts.Wait != nil {
			waitSecs := *r.baseOpts.Wait
			elapsed := time.Since(r.lastRecordTime) + plannedDelay
			elapsedSecs := int32(elapsed / time.Second)
			var remaining int32
			if elapsedSecs >= waitSecs {
				remaining = 0
			} else {
				remaining = waitSecs - elapsedSecs
			}
			opts.Wait = Int32(remaining)
		}
	}

	if opts.Count == nil && opts.Bytes == nil && opts.Wait == nil && opts.SeqNum == nil && opts.Timestamp == nil && opts.TailOffset == nil && opts.Until == nil && opts.Clamp == nil {
		return nil
	}
	return opts
}

func (r *streamReader) runOnce(ctx context.Context, opts *ReadOptions) error {
	if opts != nil {
		logInfo(r.logger, "s2 read session run once args",
			"stream", string(r.streamClient.name),
			"seq_num", opts.SeqNum,
			"count", opts.Count,
			"bytes", opts.Bytes,
			"wait", opts.Wait,
			"clamp", opts.Clamp,
		)
	}

	path := fmt.Sprintf("/streams/%s/records", url.PathEscape(string(r.streamClient.name)))
	if query := buildReadQueryParams(opts); query != "" {
		path += "?" + query
	}
	reqURL := r.streamClient.basinClient.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+r.streamClient.basinClient.accessToken)
	req.Header.Set("Accept", "application/protobuf")
	req.Header.Set("Accept-Encoding", "zstd, gzip")
	req.Header.Set("Content-Type", "s2s/proto")
	if basinName := r.streamClient.basinClient.basinHeaderValue(); basinName != "" {
		req.Header.Set("s2-basin", basinName)
	}

	resp, err := r.streamClient.getHTTPClient().Do(req)
	if err != nil {
		if isStreamResetError(err) {
			logError(r.logger, "s2 read session stream reset", "stream", string(r.streamClient.name), "error", err)
			return makeStreamResetError(err, "read session")
		}

		logError(r.logger, "s2 read session connect error", "stream", string(r.streamClient.name), "error", err)
		return fmt.Errorf("failed to connect: %w", err)
	}
	if resp.StatusCode >= 400 {
		logError(r.logger, "s2 read session http status", "stream", string(r.streamClient.name), "status", resp.StatusCode)
		return parseHTTPError(resp)
	}

	r.bodyMu.Lock()
	r.respBody = resp.Body
	r.bodyMu.Unlock()
	defer r.closeRespBody()

	frameReader := internalframing.NewFrameReader(resp.Body)
	tailTimer := time.NewTimer(tailWatchdogTimeout)
	defer tailTimer.Stop()

	resetTailTimer := func() {
		if !tailTimer.Stop() {
			select {
			case <-tailTimer.C:
			default:
			}
		}
		tailTimer.Reset(tailWatchdogTimeout)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.closed:
			return errSessionClosed
		case <-tailTimer.C:
			return &S2Error{
				Message: fmt.Sprintf("no batch received for %.0f seconds", tailWatchdogTimeout.Seconds()),
				Status:  408,
				Code:    "TIMEOUT",
			}
		default:
		}

		frame, err := frameReader.ReadFrame()
		if err != nil {
			if err == io.EOF || isExpectedFrameReadError(err) {
				return nil
			}
			logError(r.logger, "s2 read session frame error", "stream", string(r.streamClient.name), "error", err)
			return fmt.Errorf("read frame error: %w", err)
		}

		body, err := frame.DecompressedBody()
		if err != nil {
			logError(r.logger, "s2 read session frame decompress error", "stream", string(r.streamClient.name), "error", err)
			return err
		}

		if frame.Terminal {
			if frame.StatusCode != nil && *frame.StatusCode >= 400 {
				logError(r.logger, "s2 read session terminal error", "stream", string(r.streamClient.name), "status", *frame.StatusCode)
				return decodeAPIError(*frame.StatusCode, body)
			}
			logInfo(r.logger, "s2 read session terminal ok", "stream", string(r.streamClient.name), "status", frame.StatusCode)
			return nil
		}

		batch, err := parseReadBatch(body)
		if err != nil {
			logError(r.logger, "s2 read session batch parse error", "stream", string(r.streamClient.name), "error", err)
			return err
		}
		logInfo(r.logger, "s2 read session batch", "stream", string(r.streamClient.name), "records", len(batch.Records))
		resetTailTimer()

		if batch.Tail != nil {
			logInfo(r.logger, "s2 read session batch tail", "stream", string(r.streamClient.name), "seq_num", batch.Tail.SeqNum)
			r.stateMu.Lock()
			r.lastTail = batch.Tail
			r.stateMu.Unlock()
		}

		for _, record := range batch.Records {
			if err := r.handleRecord(ctx, record); err != nil {
				return err
			}
		}
	}
}

func (r *streamReader) handleRecord(ctx context.Context, record SequencedRecord) error {
	select {
	case r.recordsCh <- record:
	case <-r.closed:
		return errSessionClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	logInfo(r.logger, "s2 read session record", "stream", string(r.streamClient.name), "seq_num", record.SeqNum)

	r.recordsRead++
	r.bytesRead += MeteredSequencedRecordBytes(record)
	r.lastRecordTime = time.Now()
	r.stateMu.Lock()
	r.nextSeq = record.SeqNum + 1
	r.hasNextSeq = true
	r.nextTS = record.Timestamp
	r.stateMu.Unlock()

	return nil
}

func (r *streamReader) limitsReached() bool {
	if r.baseOpts == nil {
		return false
	}
	if r.baseOpts.Count != nil && r.recordsRead >= *r.baseOpts.Count {
		return true
	}
	if r.baseOpts.Bytes != nil && r.bytesRead >= *r.baseOpts.Bytes {
		return true
	}
	return false
}

func (r *streamReader) sendError(err error) {
	logError(r.logger, "s2 read session error forwarded", "stream", string(r.streamClient.name), "error", err)
	select {
	case r.errorCh <- err:
	case <-r.closed:
	case <-r.ctx.Done():
	}
}
