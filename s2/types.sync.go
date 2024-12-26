// Auto generated file. DO NOT EDIT.

package s2

import (
	"time"
)

// Current state of the basin.
type BasinState uint

const (
	// Unspecified.
	BasinStateUnspecified BasinState = iota
	// Basin is active.
	BasinStateActive
	// Basin is being created.
	BasinStateCreating
	// Basin is being deleted.
	BasinStateDeleting
)

// Basin information.
type BasinInfo struct {
	// Basin name.
	Name string
	// Basin scope.
	Scope string
	// Cell assignment.
	Cell string
	// Basin state.
	State BasinState
}

// List basins request.
type ListBasinsRequest struct {
	// List basin names that begin with this prefix.
	Prefix string
	// Only return basins names that lexicographically start after this name.
	// This can be the last basin name seen in a previous listing, to continue from there.
	// It must be greater than or equal to the prefix if specified.
	StartAfter string
	// Number of results, upto a maximum of 1000.
	Limit uint64
}

// List basins response.
type ListBasinsResponse struct {
	// Matching basins.
	Basins []BasinInfo
	// If set, indicates there are more results that can be listed with `start_after`.
	HasMore bool
}

// Stream information.
type StreamInfo struct {
	// Stream name.
	Name string
	// Creation time in seconds since Unix epoch.
	CreatedAt time.Time
	// Deletion time in seconds since Unix epoch, if the stream is being deleted.
	DeletedAt *time.Time
}

// List streams request.
type ListStreamsRequest struct {
	// List stream names that begin with this prefix.
	Prefix string
	// Only return stream names that lexicographically start after this name.
	// This can be the last stream name seen in a previous listing, to continue from there.
	// It must be greater than or equal to the prefix if specified.
	StartAfter string
	// Number of results, upto a maximum of 1000.
	Limit uint64
}

// List streams response.
type ListStreamsResponse struct {
	// Matching streams.
	Streams []StreamInfo
	// If set, indicates there are more results that can be listed with `start_after`.
	HasMore bool
}

// Storage class for recent writes.
type StorageClass uint

const (
	// Unspecified, which is currently overridden to `STORAGE_CLASS_EXPRESS`.
	StorageClassUnspecified StorageClass = iota
	// Standard, which offers end-to-end latencies under 500 ms.
	StorageClassStandard
	// Express, which offers end-to-end latencies under 50 ms.
	StorageClassExpress
)

// Age in seconds for automatic trimming of records older than this threshold.
// If set to 0, the stream will have infinite retention.
type RetentionPolicyAge time.Duration

// Stream configuration.
type StreamConfig struct {
	// Storage class for recent writes. This is the main cost:performance knob in S2.
	StorageClass StorageClass
	// Retention policy for the stream.
	// If unspecified, the default is to retain records for 7 days.
	//
	// Valid types for RetentionPolicy are:
	// 	- `RetentionPolicyAge`
	RetentionPolicy implRetentionPolicy
}

// Basin configuration.
type BasinConfig struct {
	// Default stream configuration.
	DefaultStreamConfig *StreamConfig
}

// Create basin request.
type CreateBasinRequest struct {
	// Basin name, which must be globally unique. It can be omitted to let the service assign a unique name.
	// The name must be between 8 and 48 characters, comprising lowercase letters, numbers and hyphens.
	// It cannot begin or end with a hyphen.
	Basin string
	// Basin configuration.
	Config *BasinConfig
	// TODO: Assignment when implemented
}

// Delete basin request.
type DeleteBasinRequest struct {
	// Name of the basin to delete.
	Basin string
	// Delete basin if it exists else do nothing.
	IfExists bool
}

// Reconfigure basin request.
type ReconfigureBasinRequest struct {
	// Basin name.
	Basin string
	// Basin configuration.
	Config *BasinConfig
	// Specifies the pieces of configuration being updated.
	// See https://protobuf.dev/reference/protobuf/google.protobuf/#field-mask
	Mask []string
}
