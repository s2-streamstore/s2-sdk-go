package s2

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

const (
	maxEncryptionKeyHeaderValueLen = 44
	s2EncryptionKeyHeader          = "s2-encryption-key"
)

// Encryption key material for append and read operations on encrypted streams.
type EncryptionKey struct {
	material *encryptionKeyMaterial
}

type encryptionKeyMaterial struct {
	encoded [maxEncryptionKeyHeaderValueLen]byte
	length  int
}

// Create a new [EncryptionKey] from base64-encoded key material.
func NewEncryptionKey(key string) (EncryptionKey, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" || len(normalized) > maxEncryptionKeyHeaderValueLen {
		return EncryptionKey{}, invalidEncryptionKeyLength(len(normalized))
	}

	material := newEncryptionKeyMaterial(len(normalized))
	copy(material.encoded[:], normalized)

	return EncryptionKey{material: material}, nil
}

// Create a new [EncryptionKey] from raw key material.
func NewEncryptionKeyFromBytes(key []byte) (EncryptionKey, error) {
	encodedLen := base64.StdEncoding.EncodedLen(len(key))
	if encodedLen == 0 || encodedLen > maxEncryptionKeyHeaderValueLen {
		return EncryptionKey{}, invalidEncryptionKeyLength(len(key))
	}

	material := newEncryptionKeyMaterial(encodedLen)
	base64.StdEncoding.Encode(material.encoded[:encodedLen], key)

	return EncryptionKey{material: material}, nil
}

func newEncryptionKeyMaterial(length int) *encryptionKeyMaterial {
	material := &encryptionKeyMaterial{length: length}
	runtime.SetFinalizer(material, func(material *encryptionKeyMaterial) {
		clear(material.encoded[:])
		material.length = 0
	})
	return material
}

func (k EncryptionKey) String() string {
	if k.isZero() {
		return "EncryptionKey(<invalid>)"
	}

	return "EncryptionKey(<redacted>)"
}

func (k EncryptionKey) GoString() string {
	return k.String()
}

func (k EncryptionKey) headerValue() string {
	if k.material == nil {
		return ""
	}

	return string(k.material.encoded[:k.material.length])
}

func (k EncryptionKey) isZero() bool {
	return k.material == nil || k.material.length == 0
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
