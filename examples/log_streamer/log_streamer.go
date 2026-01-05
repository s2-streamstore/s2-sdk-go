package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

type LogLevel string

const (
	LogLevelDebug LogLevel = "DEBUG"
	LogLevelInfo  LogLevel = "INFO"
	LogLevelWarn  LogLevel = "WARN"
	LogLevelError LogLevel = "ERROR"
)

type LogEntry struct {
	Timestamp int64                  `json:"timestamp"`
	Level     LogLevel               `json:"level"`
	Message   string                 `json:"message"`
	Component string                 `json:"component"`
	ClusterID string                 `json:"cluster_id"`
	PodName   string                 `json:"pod_name"`
	Namespace string                 `json:"namespace"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

type LogStreamer struct {
	logger       *slog.Logger
	streamClient *s2.StreamClient
	logChan      chan LogEntry
	batchSize    int
	lingerTime   time.Duration
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	clusterID    string
	podName      string
	namespace    string
	shuttingDown bool
	mu           sync.RWMutex
}

type LogStreamerConfig struct {
	BufferSize uint
	BatchSize  int
	LingerTime time.Duration
}

func NewLogStreamer(ctx context.Context, logger *slog.Logger, streamClient *s2.StreamClient, config LogStreamerConfig) (*LogStreamer, error) {
	if config.BufferSize == 0 {
		config.BufferSize = 10000
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.LingerTime == 0 {
		config.LingerTime = 5 * time.Second
	}

	clusterID := os.Getenv("CLUSTER_ID")
	if clusterID == "" {
		clusterID = "unknown"
	}

	podName := os.Getenv("HOSTNAME")
	if podName == "" {
		hostname, _ := os.Hostname()
		podName = hostname
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	streamerCtx, cancel := context.WithCancel(ctx)

	streamer := &LogStreamer{
		logger:       logger,
		streamClient: streamClient,
		logChan:      make(chan LogEntry, config.BufferSize),
		batchSize:    config.BatchSize,
		lingerTime:   config.LingerTime,
		ctx:          streamerCtx,
		cancel:       cancel,
		clusterID:    clusterID,
		podName:      podName,
		namespace:    namespace,
	}

	logger.Info("created log streamer",
		"cluster_id", clusterID,
		"pod_name", podName,
		"namespace", namespace,
		"buffer_size", config.BufferSize,
		"batch_size", config.BatchSize,
		"linger_time", config.LingerTime)

	return streamer, nil
}

func (ls *LogStreamer) Start() error {
	ls.logger.Info("starting log streamer")

	session, err := ls.streamClient.AppendSession(ls.ctx, nil)
	if err != nil {
		ls.logger.Error("failed to start S2 append session", "error", err)

		return fmt.Errorf("failed to start append session: %w", err)
	}

	batcher := s2.NewBatcher(ls.ctx, &s2.BatchingOptions{
		MaxRecords: ls.batchSize,
		Linger:     ls.lingerTime,
	})
	producer := s2.NewProducer(ls.ctx, batcher, session)

	ls.wg.Add(1)

	go ls.sendLogs(producer, session)

	ls.logger.Info("log streamer started successfully")

	return nil
}

func (ls *LogStreamer) sendLogs(producer *s2.Producer, session *s2.AppendSession) {
	defer ls.wg.Done()
	defer producer.Close()
	defer session.Close()

	ls.logger.Info("starting log sender goroutine")

	for {
		select {
		case <-ls.ctx.Done():
			ls.logger.Info("log sender shutting down due to context cancellation")

			return

		case logEntry, ok := <-ls.logChan:
			if !ok {
				ls.logger.Info("log channel closed, shutting down sender")

				return
			}

			record, err := ls.logEntryToAppendRecord(logEntry)
			if err != nil {
				ls.logger.Error("failed to convert log entry to append record",
					"error", err,
					"message", logEntry.Message)

				continue
			}

			fut, err := producer.Submit(record)
			if err != nil {
				ls.logger.Error("failed to submit log record to producer",
					"error", err,
					"message", logEntry.Message)

				continue
			}

			go func(msg string) {
				ticket, err := fut.Wait(ls.ctx)
				if err != nil {
					ls.logger.Error("failed to enqueue log record",
						"error", err,
						"message", msg)

					return
				}
				ack, err := ticket.Ack(ls.ctx)
				if err != nil {
					ls.logger.Error("failed to get ack for log record",
						"error", err,
						"message", msg)

					return
				}
				ls.logger.Debug("received acknowledgment from S2",
					"seq_num", ack.SeqNum())
			}(logEntry.Message)
		}
	}
}

func (ls *LogStreamer) logEntryToAppendRecord(entry LogEntry) (s2.AppendRecord, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return s2.AppendRecord{}, fmt.Errorf("failed to marshal log entry: %w", err)
	}

	timestamp := uint64(entry.Timestamp)

	headers := []s2.Header{
		s2.NewHeader("level", string(entry.Level)),
		s2.NewHeader("component", entry.Component),
		s2.NewHeader("cluster_id", entry.ClusterID),
		s2.NewHeader("pod_name", entry.PodName),
		s2.NewHeader("namespace", entry.Namespace),
	}

	return s2.AppendRecord{
		Timestamp: &timestamp,
		Headers:   headers,
		Body:      body,
	}, nil
}

func (ls *LogStreamer) StreamLog(entry LogEntry) error {
	ls.mu.RLock()
	shuttingDown := ls.shuttingDown
	ls.mu.RUnlock()

	if shuttingDown {
		return fmt.Errorf("log streamer is shutting down")
	}

	if entry.ClusterID == "" {
		entry.ClusterID = ls.clusterID
	}
	if entry.PodName == "" {
		entry.PodName = ls.podName
	}
	if entry.Namespace == "" {
		entry.Namespace = ls.namespace
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}

	select {
	case ls.logChan <- entry:
		return nil
	default:
		return fmt.Errorf("log channel is full, dropping log entry")
	}
}

func (ls *LogStreamer) StreamLogBlocking(entry LogEntry) error {
	ls.mu.RLock()
	shuttingDown := ls.shuttingDown
	ls.mu.RUnlock()

	if shuttingDown {
		return fmt.Errorf("log streamer is shutting down")
	}

	if entry.ClusterID == "" {
		entry.ClusterID = ls.clusterID
	}
	if entry.PodName == "" {
		entry.PodName = ls.podName
	}
	if entry.Namespace == "" {
		entry.Namespace = ls.namespace
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}

	select {
	case ls.logChan <- entry:
		return nil
	case <-ls.ctx.Done():
		return fmt.Errorf("context cancelled while sending log")
	}
}

func (ls *LogStreamer) Stop() error {
	ls.logger.Info("stopping log streamer")

	ls.mu.Lock()
	ls.shuttingDown = true
	ls.mu.Unlock()

	close(ls.logChan)
	ls.cancel()
	ls.wg.Wait()

	ls.logger.Info("log streamer stopped successfully")

	return nil
}

func (ls *LogStreamer) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"buffer_size":    cap(ls.logChan),
		"queued_logs":    len(ls.logChan),
		"batch_size":     ls.batchSize,
		"linger_time_ms": ls.lingerTime.Milliseconds(),
		"cluster_id":     ls.clusterID,
		"pod_name":       ls.podName,
		"namespace":      ls.namespace,
	}
}
