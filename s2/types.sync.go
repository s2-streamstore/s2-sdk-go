// Auto generated file. DO NOT EDIT.

package s2

import (
	"fmt"
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
	// Defaults to `STORAGE_CLASS_EXPRESS`.
	StorageClassUnspecified StorageClass = iota
	// Standard, which offers end-to-end latencies under 500 ms.
	StorageClassStandard
	// Express, which offers end-to-end latencies under 50 ms.
	StorageClassExpress
)

// Age in seconds for automatic trimming of records older than this threshold.
// If this is set to 0, the stream will have infinite retention.
// (While S2 is in public preview, this is capped at 28 days. Let us know if you'd like the cap removed.)
type RetentionPolicyAge time.Duration

// Timestamping mode.
// Note that arrival time is always in milliseconds since Unix epoch.
type TimestampingMode uint

const (
	// Defaults to `TIMESTAMPING_MODE_CLIENT_PREFER`.
	TimestampingModeUnspecified TimestampingMode = iota
	// Prefer client-specified timestamp if present otherwise use arrival time.
	TimestampingModeClientPrefer
	// Require a client-specified timestamp and reject the append if it is missing.
	TimestampingModeClientRequire
	// Use the arrival time and ignore any client-specified timestamp.
	TimestampingModeClientArrival
)

// Timestamping behavior.
type Timestamping struct {
	// Timestamping mode for appends that influences how timestamps are handled.
	Mode TimestampingMode
	// Allow client-specified timestamps to exceed the arrival time.
	// If this is false or not set, client timestamps will be capped at the arrival time.
	Uncapped *bool
}

// Retention policy for the stream.
// If unspecified, the default is to retain records for 7 days.
type RetentionPolicy interface {
	implRetentionPolicy()
}

// Stream configuration.
type StreamConfig struct {
	// Storage class for recent writes.
	StorageClass StorageClass
	// Retention policy for the stream.
	// If unspecified, the default is to retain records for 7 days.
	//
	// Valid types for RetentionPolicy are:
	// 	- `RetentionPolicyAge`
	RetentionPolicy RetentionPolicy
	// Timestamping behavior.
	Timestamping *Timestamping
}

// Basin configuration.
type BasinConfig struct {
	// Default stream configuration.
	DefaultStreamConfig *StreamConfig
	// Create stream on append if it doesn't exist,
	// using the default stream configuration.
	CreateStreamOnAppend bool
	// Create stream on read if it doesn't exist,
	// using the default stream configuration.
	CreateStreamOnRead bool
}

// Create basin request.
type CreateBasinRequest struct {
	// Basin name, which must be globally unique.
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

// Limit how many records can be retrieved.
// If both count and bytes are specified, either limit may be hit.
type ReadLimit struct {
	// Record count limit.
	Count *uint64
	// Metered bytes limit.
	Bytes *uint64
}

// Read request.
type ReadRequest struct {
	// Starting position for records.
	// Retrieved batches will start at the first record whose position is greater than or equal to it.
	Start ReadStart
	// Limit how many records can be returned.
	// This will get capped at the default limit,
	// which is up to 1000 records or 1MiB of metered bytes.
	Limit ReadLimit
	// Exclusive timestamp to read until.
	// If provided, this is applied as an additional constraint on top of the `limit`,
	// and will guarantee that all records returned have timestamps < the provided `until`.
	Until *uint64
	// Clamp the start position at the tail position.
	// If set, the read will start at the tail of the stream if the requested position is greater than it.
	Clamp bool
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
	// Timestamp for this record.
	Timestamp uint64
	// Series of name-value pairs for this record.
	Headers []Header
	// Body of this record.
	Body []byte
}

// Batch of records.
// It can only be empty when not in a session context,
// if the request cannot be satisfied without violating its limit.
type ReadOutputBatch struct {
	// Batch of sequenced records.
	Records []SequencedRecord
}

// Tail of the stream, i.e. sequence number that will be assigned to the next record.
// It will be returned if the requested starting position is greater than the tail,
// or only in case of a limited read, equal to it.
// It will also be returned if there are no records on the stream between the
// requested starting position and the tail.
type ReadOutputNextSeqNum uint64

// Output of a read.
//
// Valid types for ReadOutput are:
//   - `ReadOutputBatch`
//   - `ReadOutputNextSeqNum`
type ReadOutput interface {
	implReadOutput()
}

// Sequence number.
type ReadStartSeqNum uint64

// Timestamp.
type ReadStartTimestamp uint64

// Number of records before the tail, i.e. before the next sequence number.
type ReadStartTailOffset uint64

// Starting position for records.
// Retrieved batches will start at the first record whose position is greater than or equal to it.
//
// Valid types for ReadStart are:
//   - `ReadStartSeqNum`
//   - `ReadStartTimestamp`
//   - `ReadStartTailOffset`
type ReadStart interface {
	implReadStart()
	fmt.Stringer
}

// Record to be appended to a stream.
type AppendRecord struct {
	// Timestamp for this record.
	// Precise semantics depend on the stream's `timestamping` config.
	Timestamp *uint64
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
	FencingToken *string
}

// Output from append response.
type AppendOutput struct {
	// Sequence number of first record appended.
	StartSeqNum uint64
	// Timestamp of the first record appended.
	StartTimestamp uint64
	// Sequence number of last record appended + 1.
	// `end_seq_num - start_seq_num` will be the number of records in the batch.
	EndSeqNum uint64
	// Timestamp of the last record appended.
	EndTimestamp uint64
	// Tail of the stream, i.e. sequence number that will be assigned to the next record.
	// This can be greater than `end_seq_num` in case of concurrent appends.
	NextSeqNum uint64
	// Timestamp of the last durable record on the stream.
	LastTimestamp uint64
}

// Read session request.
type ReadSessionRequest struct {
	// Starting position for records.
	// Retrieved batches will start at the first record whose position is greater than or equal to it.
	Start ReadStart
	// Limit on how many records can be returned. When a limit is specified, the session will be terminated as soon as
	// the limit is met, or when the current tail of the stream is reached -- whichever occurs first.
	// If no limit is specified, the session will remain open after catching up to the tail, and continue tailing as
	// new messages are written to the stream.
	Limit ReadLimit
	// Heartbeats can be enabled to monitor end-to-end session health.
	// A heartbeat will be sent when the initial switch to real-time tailing happens,
	// as well as when no records are available at a randomized interval between 5 and 15 seconds.
	Heartbeats bool
	// Exclusive timestamp to read until.
	// If provided, this is applied as an additional constraint on top of the `limit`,
	// and will guarantee that all records returned have timestamps < the provided `until`.
	Until *uint64
	// Clamp the start position at the tail position.
	Clamp bool
}

// Check tail response.
type CheckTailResponse struct {
	// Sequence number that will be assigned to the next record on the stream.
	// It will be 0 for a stream that has not been written to.
	NextSeqNum uint64
	// Timestamp of the last durable record on the stream.
	// It starts out as 0 for a new stream.
	LastTimestamp uint64
}

// API operations.
type Operation uint

const (
	// Unspecified operation.
	OperationUnspecified Operation = iota
	// List basins.
	OperationListBasins
	// Create a basin.
	OperationCreateBasin
	// Delete a basin.
	OperationDeleteBasin
	// Update basin configuration.
	OperationReconfigureBasin
	// Get basin configuration.
	OperationGetBasinConfig
	// Issue an access token.
	OperationIssueAccessToken
	// Revoke an access token.
	OperationRevokeAccessToken
	// List access tokens.
	OperationListAccessTokens
	// List streams.
	OperationListStreams
	// Create a stream.
	OperationCreateStream
	// Delete a stream.
	OperationDeleteStream
	// Get stream configuration.
	OperationGetStreamConfig
	// Update stream configuration.
	OperationReconfigureStream
	// Check tail of a stream.
	OperationCheckTail
	// Append records to a stream.
	OperationAppend
	// Read records from a stream.
	OperationRead
	// Trim records up to a sequence number.
	OperationTrim
	// Set a fencing token for a stream.
	OperationFence
	// Retrieve account-level metrics.
	OperationAccountMetrics
	// Retrieve basin-level metrics.
	OperationBasinMetrics
	// Retrieve stream-level metrics.
	OperationStreamMetrics
)

// Read/Write permissions.
type ReadWritePermissions struct {
	// Read permission.
	Read bool
	// Write permission.
	Write bool
}

// Access permissions for a group.
type PermittedOperationGroups struct {
	// Access permissions at account level.
	Account *ReadWritePermissions
	// Access permissions at basin level.
	Basin *ReadWritePermissions
	// Access permissions at stream level.
	Stream *ReadWritePermissions
}

// Set of named resources.
//
// Valid types for ResourceSet are:
//   - `ResourceSetExact`
//   - `ResourceSetPrefix`
type ResourceSet interface {
	implResourceSet()
	fmt.Stringer
}

// Match only the resource with this exact name.
// Use an empty string to match no resources.
type ResourceSetExact string

// Match all resources that start with this prefix.
// Use an empty string to match all resource.
type ResourceSetPrefix string

// Access token scope.
type AccessTokenScope struct {
	// Basin names allowed.
	Basins ResourceSet
	// Stream names allowed.
	Streams ResourceSet
	// Token IDs allowed.
	AccessTokens ResourceSet
	// Access permissions at operation group level.
	OpGroups *PermittedOperationGroups
	// Operations allowed for the token.
	// A union of allowed operations and groups is used as an effective set of allowed operations.
	Ops []Operation
}

// Access token information.
type AccessTokenInfo struct {
	// Access token ID.
	// It must be unique to the account and between 1 and 96 characters.
	ID string
	// Expiration time in seconds since Unix epoch.
	// If not set, the expiration will be set to that of the requestor's token.
	ExpiresAt *time.Time
	// Namespace streams based on the configured stream-level scope, which must be a prefix.
	// Stream name arguments will be automatically prefixed, and the prefix will be stripped
	// when listing streams.
	AutoPrefixStreams bool
	// Access token scope.
	Scope *AccessTokenScope
}

// List access tokens request.
type ListAccessTokensRequest struct {
	// List access tokens that begin with this prefix.
	Prefix string
	// Only return access tokens that lexicographically start after this ID.
	StartAfter string
	// Number of results, up to a maximum of 1000.
	Limit *uint64
}

// List access tokens response.
type ListAccessTokensResponse struct {
	// Access tokens information.
	AccessTokens []AccessTokenInfo
	// If set, indicates there are more results that can be listed with `start_after`.
	HasMore bool
}
