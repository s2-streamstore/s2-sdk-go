package s2

import "github.com/google/uuid"

// Returns a pointer to v.
func Uint64(v uint64) *uint64 {
	return &v
}

// Returns a pointer to v.
func Int64(v int64) *int64 {
	return &v
}

// Returns a pointer to v.
func Int32(v int32) *int32 {
	return &v
}

// Returns a pointer to v.
func Uint32(v uint32) *uint32 {
	return &v
}

// Returns a pointer to v.
func String(v string) *string {
	return &v
}

// Returns a pointer to v.
func Bool(v bool) *bool {
	return &v
}

func newRequestToken() string {
	return uuid.NewString()
}

// Returns a pointer to v for any type.
func Ptr[T any](v T) *T {
	return &v
}
