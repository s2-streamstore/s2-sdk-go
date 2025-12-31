package s2

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"
)

const (
	// Connection manager settings
	connCleanupInterval      = 5 * time.Minute
	connDefaultMaxIdleTime   = 10 * time.Minute
	connDefaultMaxLifetime   = 30 * time.Minute
	connMaxConsecutiveErrors = 3

	// HTTP/2 transport settings
	http2MaxReadFrameSize  = 16 * 1024 * 1024
	http2MaxHeaderListSize = 10 * 1024 * 1024
	http2ReadIdleTimeout   = 30 * time.Second
	http2PingTimeout       = 15 * time.Second
	http2WriteByteTimeout  = 30 * time.Second
)

type connectionManager struct {
	clients map[string]*http2ClientInfo
	mutex   sync.RWMutex
}

type http2ClientInfo struct {
	client            *http.Client
	transport         *http2.Transport
	baseURL           string
	createdAt         time.Time
	lastUsed          time.Time
	consecutiveErrors int
	healthy           bool
	mutex             sync.RWMutex
	maxIdleTime       time.Duration
	maxLifetime       time.Duration
}

var globalConnectionManager = func() *connectionManager {
	cm := &connectionManager{
		clients: make(map[string]*http2ClientInfo),
	}
	cm.startCleanupRoutine()
	return cm
}()

func (cm *connectionManager) getOrCreateHTTP2Client(baseURL string, allowH2C bool, connectionTimeout time.Duration) (*http.Client, error) {
	// Include allowH2C in the cache key to avoid mixing h2c and non-h2c clients
	cacheKey := baseURL
	if allowH2C {
		cacheKey = "h2c:" + baseURL
	}

	cm.mutex.RLock()
	if clientInfo, exists := cm.clients[cacheKey]; exists {
		clientInfo.mutex.RLock()
		healthy := clientInfo.healthy
		clientInfo.mutex.RUnlock()

		if healthy {
			cm.mutex.RUnlock()
			clientInfo.mutex.Lock()
			clientInfo.lastUsed = time.Now()
			clientInfo.mutex.Unlock()
			return clientInfo.client, nil
		}
	}
	cm.mutex.RUnlock()

	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if clientInfo, exists := cm.clients[cacheKey]; exists {
		clientInfo.mutex.RLock()
		healthy := clientInfo.healthy
		clientInfo.mutex.RUnlock()
		if healthy {
			return clientInfo.client, nil
		}
	}

	clientInfo := cm.createHTTP2ClientInfo(baseURL, allowH2C, connectionTimeout)
	cm.clients[cacheKey] = clientInfo

	return clientInfo.client, nil
}

func (cm *connectionManager) startCleanupRoutine() {
	ticker := time.NewTicker(connCleanupInterval)
	go func() {
		for range ticker.C {
			cm.cleanupStaleConnections()
		}
	}()
}

func (cm *connectionManager) cleanupStaleConnections() {
	now := time.Now()
	var toRemove []string

	cm.mutex.RLock()
	for baseURL, clientInfo := range cm.clients {
		clientInfo.mutex.RLock()

		maxLifetime := clientInfo.maxLifetime
		if maxLifetime == 0 {
			maxLifetime = connDefaultMaxLifetime
		}

		maxIdleTime := clientInfo.maxIdleTime
		if maxIdleTime == 0 {
			maxIdleTime = connDefaultMaxIdleTime
		}

		shouldRemove := now.Sub(clientInfo.createdAt) > maxLifetime ||
			now.Sub(clientInfo.lastUsed) > maxIdleTime ||
			!clientInfo.healthy

		clientInfo.mutex.RUnlock()

		if shouldRemove {
			toRemove = append(toRemove, baseURL)
		}
	}
	cm.mutex.RUnlock()

	for _, baseURL := range toRemove {
		cm.removeClient(baseURL)
	}
}

func (cm *connectionManager) createHTTP2ClientInfo(baseURL string, allowH2C bool, connectionTimeout time.Duration) *http2ClientInfo {
	dialer := &net.Dialer{
		Timeout: connectionTimeout,
	}

	transport := &http2.Transport{
		AllowHTTP:                  allowH2C,
		MaxReadFrameSize:           http2MaxReadFrameSize,
		MaxHeaderListSize:          http2MaxHeaderListSize,
		ReadIdleTimeout:            http2ReadIdleTimeout,
		PingTimeout:                http2PingTimeout,
		WriteByteTimeout:           http2WriteByteTimeout,
		StrictMaxConcurrentStreams: false,
		CountError:                 func(errType string) { _ = errType },
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			// For h2c (HTTP/2 cleartext), use plain TCP
			if allowH2C {
				return dialer.DialContext(ctx, network, addr)
			}
			// For TLS, dial then handshake
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(conn, cfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0,
	}

	now := time.Now()

	return &http2ClientInfo{
		client:      client,
		transport:   transport,
		baseURL:     baseURL,
		createdAt:   now,
		lastUsed:    now,
		healthy:     true,
		maxIdleTime: connDefaultMaxIdleTime,
		maxLifetime: connDefaultMaxLifetime,
	}
}

func (cm *connectionManager) markClientUnhealthy(baseURL string, err error) {
	cm.mutex.RLock()
	clientInfo, exists := cm.clients[baseURL]
	cm.mutex.RUnlock()

	if !exists {
		return
	}

	clientInfo.mutex.Lock()
	defer clientInfo.mutex.Unlock()

	clientInfo.consecutiveErrors++

	if clientInfo.consecutiveErrors >= connMaxConsecutiveErrors {
		clientInfo.healthy = false
	}

	if isConnectionError(err) {
		clientInfo.healthy = false
	}

	if isStreamResetError(err) {
		clientInfo.healthy = false
		clientInfo.consecutiveErrors = connMaxConsecutiveErrors
	}
}

func (cm *connectionManager) markClientHealthy(baseURL string) {
	cm.mutex.RLock()
	clientInfo, exists := cm.clients[baseURL]
	cm.mutex.RUnlock()

	if !exists {
		return
	}

	clientInfo.mutex.Lock()
	defer clientInfo.mutex.Unlock()

	clientInfo.consecutiveErrors = 0
	clientInfo.healthy = true
	clientInfo.lastUsed = time.Now()
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var errno syscall.Errno
		if errors.As(opErr.Err, &errno) {
			switch errno {
			case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE:
				return true
			}
		}
		return true
	}

	var goaway *http2.GoAwayError
	return errors.As(err, &goaway)
}

func isStreamResetError(err error) bool {
	var se http2.StreamError
	return errors.As(err, &se)
}

func (cm *connectionManager) removeClient(baseURL string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if clientInfo, exists := cm.clients[baseURL]; exists {
		if clientInfo.transport != nil {
			clientInfo.transport.CloseIdleConnections()
		}
		delete(cm.clients, baseURL)
	}
}
