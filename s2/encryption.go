package s2

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

const (
	maxEncryptionKeyHeaderValueLen = 44
	s2EncryptionKeyHeader          = "s2-encryption-key"
)

// Encryption key material for append and read operations on encrypted streams.
type EncryptionKey struct {
	encoded string
}

// Create a new [EncryptionKey] from base64-encoded key material.
func NewEncryptionKey(key string) (EncryptionKey, error) {
	normalized := strings.TrimSpace(key)
	return newEncryptionKey(normalized, len(normalized))
}

// Create a new [EncryptionKey] from raw key material.
func NewEncryptionKeyFromBytes(key []byte) (EncryptionKey, error) {
	encoded := base64.StdEncoding.EncodeToString(key)
	return newEncryptionKey(encoded, len(key))
}

func newEncryptionKey(encoded string, invalidLength int) (EncryptionKey, error) {
	if encoded == "" || len(encoded) > maxEncryptionKeyHeaderValueLen {
		return EncryptionKey{}, invalidEncryptionKeyLength(invalidLength)
	}

	return EncryptionKey{encoded: encoded}, nil
}

func (k EncryptionKey) String() string {
	if k.encoded == "" {
		return "EncryptionKey(<invalid>)"
	}

	return "EncryptionKey(<redacted>)"
}

func (k EncryptionKey) GoString() string {
	return k.String()
}

func (k EncryptionKey) headerValue() string {
	return k.encoded
}

func (k EncryptionKey) isZero() bool {
	return k.encoded == ""
}

func invalidEncryptionKeyLength(length int) error {
	return newValidationError(
		fmt.Sprintf("invalid encryption key: key material length %d is out of range", length),
	)
}

func setEncryptionKeyHeader(headers http.Header, key *EncryptionKey) {
	if key == nil {
		return
	}

	headers.Set(s2EncryptionKeyHeader, key.headerValue())
}

func encryptionHeaders(key *EncryptionKey) map[string]string {
	if key == nil {
		return nil
	}

	return map[string]string{s2EncryptionKeyHeader: key.headerValue()}
}
