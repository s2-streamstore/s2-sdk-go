// Auto generated file. DO NOT EDIT.

package s2

import (
	"time"
)

// Basin scope.
type BasinScope uint

const (
	// Unspecified basin scope.
	BasinScopeUnspecified BasinScope = iota
	// aws us-east-1 region.
	BasinScopeAwsUSEast1
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
	Scope BasinScope
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
	// Number of results, up to a maximum of 1000.
	Limit *uint64
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
	// Number of results, up to a maximum of 1000.
	Limit *uint64
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
	// Controls how to handle timestamps when they are not provided by the client.
	// If this is false (or not set), the record's arrival time will be assigned as its timestamp.
	// If this is true, then any append without a client-specified timestamp will be rejected as invalid.
	RequireClientTimestamps bool
}

// Basin configuration.
type BasinConfig struct {
	// Default stream configuration.
	DefaultStreamConfig *StreamConfig
	// Create stream on append if it doesn't exist,
	// using the default stream configuration.
	CreateStreamOnAppend bool
}

// Create basin request.
type CreateBasinRequest struct {
	// Basin name, which must be globally unique. It can be omitted to let the service assign a unique name.
	// The name must be between 8 and 48 characters, comprising lowercase letters, numbers and hyphens.
	// It cannot begin or end with a hyphen.
	Basin string
	// Basin configuration.
	Config *BasinConfig
	// Basin scope.
	Scope BasinScope
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

// Create stream request.
type CreateStreamRequest struct {
	// Stream name, which must be unique within the basin.
	// It can be an arbitrary string upto 512 characters.
	// Backslash (`/`) is recommended as a delimiter for hierarchical naming.
	Stream string
	// Configuration for the new stream.
	Config *StreamConfig
}

// Delete stream request.
type DeleteStreamRequest struct {
	// Stream name.
	Stream string
	// Delete stream if it exists else do nothing.
	IfExists bool
}

// Reconfigure stream request.
type ReconfigureStreamRequest struct {
	// Stream name.
	Stream string
	// Stream configuration with updated values.
	Config *StreamConfig
	// Specifies the pieces of configuration being updated.
	// See https://protobuf.dev/reference/protobuf/google.protobuf/#field-mask
	Mask []string
}

// If both count and bytes are specified, either limit may be hit.
type ReadLimit struct {
	// Record count limit.
	Count *uint64
	// Metered bytes limit.
	Bytes *uint64
}

// Read request.
type ReadRequest struct {
	// Starting sequence number (inclusive).
	StartSeqNum uint64
	// Limit how many records can be returned.
	// This will get capped at the default limit,
	// which is up to 1000 records or 1MiB of metered bytes.
	Limit ReadLimit
}

// Headers add structured information to a record as name-value pairs.
type Header struct {
	// Header name blob.
	// The name cannot be empty, with the exception of an S2 command record.
	Name []byte
	// Header value blob.
	Value []byte
}

// Record retrieved from a stream.
type SequencedRecord struct {
	// Sequence number assigned to this record.
	SeqNum uint64
	// Timestamp for this record in milliseconds since Unix epoch.
	Timestamp time.Time
	// Series of name-value pairs for this record.
	Headers []Header
	// Body of this record.
	Body []byte
}

// A batch of sequenced records.
type SequencedRecordBatch struct {
	// Batch of sequenced records.
	Records []SequencedRecord
}

// Batch of records.
// It can only be empty when not in a session context (which implies a limit),
// if the first record that could have been retrieved would violate the limit.
type ReadOutputBatch struct {
	*SequencedRecordBatch
}

// Sequence number for the first record on this stream.
// Typically this will be returned when the requested `start_seq_num` was smaller.
// It may also be returned during a session, if the stream gets concurrently trimmed.
type ReadOutputFirstSeqNum uint64

// Sequence number that will be assigned to the next record on this stream.
// This will be returned either because the requested `start_seq_num` was larger,
// or in case of a limited read, equal to it.
type ReadOutputNextSeqNum uint64

// Output from read response.
//
// Valid types for ReadOutput are:
//   - `ReadOutputBatch`
//   - `ReadOutputFirstSeqNum`
//   - `ReadOutputNextSeqNum`
type ReadOutput interface {
	implReadOutput()
}

// Record to be appended to a stream.
type AppendRecord struct {
	// Timestamp for this record in milliseconds since Unix epoch.
	// The service ensures monotonicity by adjusting it up if necessary to the maximum observed timestamp.
	// A timestamp detected to be in the future will be adjusted down.
	// If not provided, the semantics depend on the stream's `require_client_timestamps` config.
	Timestamp *time.Time
	// Series of name-value pairs for this record.
	Headers []Header
	// Body of this record.
	Body []byte
}

// Input for append requests.
type AppendInput struct {
	// Batch of records to append atomically, which must contain at least one record, and no more than 1000.
	// The total size of a batch of records may not exceed 1MiB of metered bytes.
	Records *AppendRecordBatch
	// Enforce that the sequence number issued to the first record matches.
	MatchSeqNum *uint64
	// Enforce a fencing token which must have been previously set by a `fence` command record.
	FencingToken []byte
}

// Output from append response.
type AppendOutput struct {
	// Sequence number of first record appended.
	StartSeqNum uint64
	// Sequence number of last durable record on the stream + 1.
	// This can be greater than `end_seq_num` in case of concurrent appends.
	NextSeqNum uint64
	// Sequence number of last record appended + 1.
	// `end_seq_num - start_seq_num` will be the number of records in the batch.
	EndSeqNum uint64
}

// Read session request.
type ReadSessionRequest struct {
	// Starting sequence number (inclusive).
	StartSeqNum uint64
	// Limit on how many records can be returned. When a limit is specified, the session will be terminated as soon as
	// the limit is met, or when the current tail of the stream is reached -- whichever occurs first.
	// If no limit is specified, the session will remain open after catching up to the tail, and continue tailing as
	// new messages are written to the stream.
	Limit ReadLimit
}
