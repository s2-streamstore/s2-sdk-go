package s2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

const maxErrorBodyBytes = 64 * 1024 // 64 KiB

type httpClient struct {
	client      *http.Client
	baseURL     string
	accessToken string
	logger      *slog.Logger
	basinName   string
	compression CompressionType
}

func (h *httpClient) request(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	return h.requestWithHeaders(ctx, method, path, body, result, nil)
}

func (h *httpClient) requestWithHeaders(ctx context.Context, method, path string, body interface{}, result interface{}, extraHeaders map[string]string) error {
	logInfo(h.logger, "s2 http request",
		"method", method,
		"path", path,
		"url", h.baseURL+path,
		"extra_headers", len(extraHeaders),
	)

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	url := h.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+h.accessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if h.basinName != "" {
		req.Header.Set("s2-basin", h.basinName)
	}

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		logError(h.logger, "s2 http perform error", "error", err, "method", method, "path", path)
		return fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	logInfo(h.logger, "s2 http response", "method", method, "path", path, "status", resp.StatusCode)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		apiErr := decodeAPIError(resp.StatusCode, body)
		logError(h.logger, "s2 http error response", "method", method, "path", path, "status", resp.StatusCode, "message", apiErr.Error())
		return apiErr
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			logError(h.logger, "s2 http decode response error", "error", err, "method", method, "path", path)
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

func (h *httpClient) requestProto(ctx context.Context, method, path string, body proto.Message, result proto.Message) error {
	const protoContentType = "application/protobuf"
	logInfo(h.logger, "s2 http proto request",
		"method", method,
		"path", path,
		"url", h.baseURL+path,
		"compression", h.compression.ContentEncoding(),
	)

	var reqBody io.Reader
	if body != nil {
		data, err := proto.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal proto body: %w", err)
		}

		if h.compression != CompressionNone {
			data, err = internalframing.Compress(data, h.compression)
			if err != nil {
				return fmt.Errorf("compress request body: %w", err)
			}
		}

		reqBody = bytes.NewReader(data)
	}

	url := h.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+h.accessToken)
	if body != nil {
		req.Header.Set("Content-Type", protoContentType)
		if encoding := h.compression.ContentEncoding(); encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
		}
	}
	if result != nil {
		req.Header.Set("Accept", protoContentType)
		req.Header.Set("Accept-Encoding", "zstd, gzip")
	}

	if h.basinName != "" {
		req.Header.Set("s2-basin", h.basinName)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		logError(h.logger, "s2 http proto perform error", "error", err, "method", method, "path", path)
		return fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	logInfo(h.logger, "s2 http proto response", "method", method, "path", path, "status", resp.StatusCode)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		apiErr := decodeAPIError(resp.StatusCode, body)
		logError(h.logger, "s2 http proto error response", "method", method, "path", path, "status", resp.StatusCode, "message", apiErr.Error())
		return apiErr
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read proto response: %w", err)
		}

		if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
			respCompression := internalframing.ParseContentEncoding(encoding)
			data, err = internalframing.Decompress(data, respCompression)
			if err != nil {
				return fmt.Errorf("decompress response body: %w", err)
			}
		}

		if err := proto.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode proto response: %w", err)
		}
	}

	return nil
}

func buildQuery(prefix, startAfter string, limit *int) string {
	params := url.Values{}
	if prefix != "" {
		params.Add("prefix", prefix)
	}
	if startAfter != "" {
		params.Add("start_after", startAfter)
	}
	if limit != nil {
		params.Add("limit", strconv.Itoa(*limit))
	}

	query := params.Encode()
	if query != "" {
		return "?" + query
	}
	return ""
}
