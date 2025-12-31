package s2

import (
	"time"
)

// Access token ID.
type AccessTokenID string

// Basin name which must be globally unique.
// It can be between 8 and 48 characters in length, and comprise lowercase letters, numbers and hyphens.
// It cannot begin or end with a hyphen.
type BasinName string
type StreamName string
type BasinScope string
type BasinState string
type MetricUnit string
type TimeseriesInterval string
type AccountMetricSet string
type BasinMetricSet string
type StreamMetricSet string
type MetricSample [2]float64
type StorageClass string
type TimestampingMode string

const (
	BasinScopeAwsUsEast1 BasinScope = "aws:us-east-1"
)

const (
	BasinStateActive   BasinState = "active"
	BasinStateCreating BasinState = "creating"
	BasinStateDeleting BasinState = "deleting"
)

const (
	MetricUnitBytes      MetricUnit = "bytes"
	MetricUnitOperations MetricUnit = "operations"
)

const (
	TimeseriesIntervalMinute TimeseriesInterval = "minute"
	TimeseriesIntervalHour   TimeseriesInterval = "hour"
	TimeseriesIntervalDay    TimeseriesInterval = "day"
)

const (
	AccountMetricSetActiveBasins AccountMetricSet = "active-basins"
	AccountMetricSetAccountOps   AccountMetricSet = "account-ops"
)

const (
	BasinMetricSetStorage          BasinMetricSet = "storage"
	BasinMetricSetAppendOps        BasinMetricSet = "append-ops"
	BasinMetricSetReadOps          BasinMetricSet = "read-ops"
	BasinMetricSetReadThroughput   BasinMetricSet = "read-throughput"
	BasinMetricSetAppendThroughput BasinMetricSet = "append-throughput"
	BasinMetricSetBasinOps         BasinMetricSet = "basin-ops"
)

const (
	StreamMetricSetStorage StreamMetricSet = "storage"
)

const (
	StorageClassStandard StorageClass = "standard"
	StorageClassExpress  StorageClass = "express"
)

const (
	TimestampingModeClientPrefer  TimestampingMode = "client-prefer"
	TimestampingModeClientRequire TimestampingMode = "client-require"
	TimestampingModeArrival       TimestampingMode = "arrival"
)

type AccessTokenInfo struct {
	// Access token ID.
	// It must be unique to the account and between 1 and 96 bytes in length.
	ID AccessTokenID `json:"id"`
	// Access token scope.
	Scope AccessTokenScope `json:"scope"`
	// Namespace streams based on the configured stream-level scope, which must be a prefix.
	// Stream name arguments will be automatically prefixed, and the prefix will be stripped when listing streams.
	AutoPrefixStreams bool `json:"auto_prefix_streams,omitempty"`
	// Expiration time.
	// If not set, the expiration will be set to that of the requestor's token.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Access token scope.
type AccessTokenScope struct {
	// Token IDs allowed.
	AccessTokens *ResourceSet `json:"access_tokens,omitempty"`
	// Basin names allowed.
	Basins *ResourceSet `json:"basins,omitempty"`
	// Access permissions at operation group level.
	OpGroups *PermittedOperationGroups `json:"op_groups,omitempty"`
	// Operations allowed for the token.
	// A union of allowed operations and groups is used as an effective set of allowed operations.
	Ops []string `json:"ops,omitempty"`
	// Stream names allowed.
	Streams *ResourceSet `json:"streams,omitempty"`
}

const (
	OperationListBasins        = "list-basins"
	OperationCreateBasin       = "create-basin"
	OperationDeleteBasin       = "delete-basin"
	OperationReconfigureBasin  = "reconfigure-basin"
	OperationGetBasinConfig    = "get-basin-config"
	OperationIssueAccessToken  = "issue-access-token"
	OperationRevokeAccessToken = "revoke-access-token"
	OperationListAccessTokens  = "list-access-tokens"
	OperationListStreams       = "list-streams"
	OperationCreateStream      = "create-stream"
	OperationDeleteStream      = "delete-stream"
	OperationGetStreamConfig   = "get-stream-config"
	OperationReconfigureStream = "reconfigure-stream"
	OperationCheckTail         = "check-tail"
	OperationAppend            = "append"
	OperationRead              = "read"
	OperationTrim              = "trim"
	OperationFence             = "fence"
	OperationAccountMetrics    = "account-metrics"
	OperationBasinMetrics      = "basin-metrics"
	OperationStreamMetrics     = "stream-metrics"
)

type ResourceSet struct {
	// Match only the resource with this exact name.
	// Use an empty string to match no resources.
	Exact *string `json:"exact,omitempty"`
	// Match all resources that start with this prefix.
	// Use an empty string to match all resource.
	Prefix *string `json:"prefix,omitempty"`
}

// Access permissions at operation group level.
type PermittedOperationGroups struct {
	// Account-level access permissions.
	Account *ReadWritePermissions `json:"account,omitempty"`
	// Basin-level access permissions.
	Basin *ReadWritePermissions `json:"basin,omitempty"`
	// Stream-level access permissions.
	Stream *ReadWritePermissions `json:"stream,omitempty"`
}

type ReadWritePermissions struct {
	// Read permission.
	Read bool `json:"read,omitempty"`
	// Write permission.
	Write bool `json:"write,omitempty"`
}

type BasinInfo struct {
	// Basin name.
	Name BasinName `json:"name"`
	// Basin scope.
	Scope BasinScope `json:"scope"`
	// Basin state.
	State BasinState `json:"state"`
}

type BasinConfig struct {
	// Create stream on append if it doesn't exist, using the default stream configuration.
	CreateStreamOnAppend *bool `json:"create_stream_on_append,omitempty"`
	// Create stream on read if it doesn't exist, using the default stream configuration.
	CreateStreamOnRead *bool `json:"create_stream_on_read,omitempty"`
	// Default stream configuration.
	DefaultStreamConfig *StreamConfig `json:"default_stream_config,omitempty"`
}

type StreamInfo struct {
	Name StreamName `json:"name"`
	// Creation time.
	CreatedAt time.Time `json:"created_at"`
	// Deletion time, if the stream is being deleted.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type StreamConfig struct {
	// Delete-on-empty configuration.
	DeleteOnEmpty *DeleteOnEmptyConfig `json:"delete_on_empty,omitempty"`
	// Retention policy for the stream.
	// If unspecified, the default is to retain records for 7 days.
	RetentionPolicy *RetentionPolicy `json:"retention_policy,omitempty"`
	// Storage class for recent writes.
	StorageClass *StorageClass `json:"storage_class,omitempty"`
	// Timestamping behavior.
	Timestamping *TimestampingConfig `json:"timestamping,omitempty"`
}

type DeleteOnEmptyConfig struct {
	// Minimum age in seconds before an empty stream can be deleted.
	// Set to 0 (default) to disable delete-on-empty (don't delete automatically).
	MinAgeSecs *int64 `json:"min_age_secs,omitempty"`
}

type RetentionPolicy struct {
	// Age limits records to a specific age window (seconds).
	Age *int64 `json:"age,omitempty"`
	// Retain records unless explicitly trimmed.
	Infinite *InfiniteRetention `json:"infinite,omitempty"`
}

// Retain records unless explicitly trimmed.
type InfiniteRetention struct{}

// Timestamping behavior.
type TimestampingConfig struct {
	// Timestamping mode for appends that influences how timestamps are handled.
	Mode *TimestampingMode `json:"mode,omitempty"`
	// "Allow client-specified timestamps to exceed the arrival time.
	// If this is `false` or not set, client timestamps will be capped at the arrival time.
	Uncapped *bool `json:"uncapped,omitempty"`
}

// Position of a record in a stream.
type StreamPosition struct {
	// Sequence number assigned by the service.
	SeqNum uint64 `json:"seq_num"`
	// Timestamp, which may be client-specified or assigned by the service.
	// If it is assigned by the service, it will represent milliseconds since Unix epoch.
	Timestamp uint64 `json:"timestamp"`
}

type TailResponse struct {
	// Sequence number that will be assigned to the next record on the stream, and timestamp of the last record.
	Tail StreamPosition `json:"tail"`
}

type BasinReconfiguration struct {
	// Create a stream on append.
	CreateStreamOnAppend *bool `json:"create_stream_on_append,omitempty"`
	// Create a stream on read.
	CreateStreamOnRead *bool `json:"create_stream_on_read,omitempty"`
	// Basin configuration.
	DefaultStreamConfig *StreamReconfiguration `json:"default_stream_config,omitempty"`
}

type StreamReconfiguration struct {
	// Delete-on-empty configuration.
	DeleteOnEmpty *DeleteOnEmptyReconfiguration `json:"delete_on_empty,omitempty"`
	// Retention policy for the stream.
	// If unspecified, the default is to retain records for 7 days.
	RetentionPolicy *RetentionPolicy `json:"retention_policy,omitempty"`
	// Storage class for recent writes.
	StorageClass *StorageClass `json:"storage_class,omitempty"`
	// Timestamping behavior.
	Timestamping *TimestampingReconfiguration `json:"timestamping,omitempty"`
}

type DeleteOnEmptyReconfiguration struct {
	// Minimum age in seconds before an empty stream can be deleted.
	// Set to 0 to disable delete-on-empty (don't delete automatically).
	MinAgeSecs *int64 `json:"min_age_secs,omitempty"`
}

type TimestampingReconfiguration struct {
	// Timestamping mode for appends that influences how timestamps are handled.
	Mode *TimestampingMode `json:"mode,omitempty"`
	// "Allow client-specified timestamps to exceed the arrival time.
	Uncapped *bool `json:"uncapped,omitempty"`
}

type ScalarMetric struct {
	// Metric name.
	Name string `json:"name"`
	// Unit of the metric.
	Unit MetricUnit `json:"unit"`
	// Metric value.
	Value float64 `json:"value"`
}

// Set of string labels.
type LabelMetric struct {
	// Label name.
	Name string `json:"name"`
	// Label values.
	Values []string `json:"values"`
}

// Named series of `(timestamp, value)` points each representing an instantaneous value.
type GaugeMetric struct {
	// Timeseries name.
	Name string `json:"name"`
	// Unit of the metric.
	Unit MetricUnit `json:"unit"`
	// Timeseries values.
	// Each element is a tuple of a timestamp in Unix epoch seconds and a data point.
	// The data point represents the value at the instant of the timestamp.
	Values []MetricSample `json:"values"`
}

type AccumulationMetric struct {
	// The duration of bucket for the accumulation.
	BucketLength TimeseriesInterval `json:"bucket_length"`
	// Timeseries name.
	Name string `json:"name"`
	// Unit of the metric.
	Unit MetricUnit `json:"unit"`
	// Timeseries values.
	// Each element is a tuple of a timestamp in Unix epoch seconds and a data point.
	// The data point represents the accumulated value for a bucket of time starting at the provided timestamp,
	// lasting for the duration of the `BucketLength` parameter.
	Values []MetricSample `json:"values"`
}

type Metric struct {
	// Single named value.
	Scalar *ScalarMetric `json:"scalar,omitempty"`
	// Named series of `(timestamp, value)` points representing an accumulation over a specified bucket.
	Accumulation *AccumulationMetric `json:"accumulation,omitempty"`
	// Named series of `(timestamp, value)` points each representing an instantaneous value.
	Gauge *GaugeMetric `json:"gauge,omitempty"`
	// Set of string labels.
	Label *LabelMetric `json:"label,omitempty"`
}

type MetricSetResponse struct {
	// Metrics comprising the set.
	Values []Metric `json:"values"`
}

type ListAccessTokensResponse struct {
	// Matching access tokens.
	AccessTokens []AccessTokenInfo `json:"access_tokens"`
	// Indicates that there are more access tokens that match the criteria.
	HasMore bool `json:"has_more"`
}

type IssueAccessTokenResponse struct {
	// Created access token.
	AccessToken string `json:"access_token"`
}

type ListBasinsResponse struct {
	// Matching basins.
	Basins []BasinInfo `json:"basins"`
	// Indicates that there are more basins that match the criteria.
	HasMore bool `json:"has_more"`
}

type ListStreamsResponse struct {
	// Matching streams.
	Streams []StreamInfo `json:"streams"`
	// Indicates that there are more results that match the criteria.
	HasMore bool `json:"has_more"`
}

// Header adds structured information to a record as a name-value pair.
type Header struct {
	// Header name.
	// The name cannot be empty, with the exception of an S2 command record.
	Name []byte
	// Header value.
	Value []byte
}

// Creates a Header from string name and value.
func NewHeader(name, value string) Header {
	return Header{Name: []byte(name), Value: []byte(value)}
}

type SequencedRecord struct {
	// Body of the record.
	Body []byte
	// Series of name-value pairs for this record.
	Headers []Header
	// Sequence number assigned by the service.
	SeqNum uint64
	// Timestamp for this record.
	Timestamp uint64
}

type ReadBatch struct {
	// Records that are durably sequenced on the stream, retrieved based on the requested criteria.
	// This can only be empty in response to a unary read (i.e. not SSE),
	// if the request cannot be satisfied without violating an explicit bound (`count`, `bytes`, or `until`).
	Records []SequencedRecord `json:"records"`
	// Sequence number that will be assigned to the next record on the stream, and timestamp of the last record.
	// This will only be present when reading recent records.
	Tail *StreamPosition `json:"tail,omitempty"`
}

// Record to be appended to a stream.
type AppendRecord struct {
	// Timestamp for this record.
	// The service will always ensure monotonicity by adjusting it up if necessary to the maximum observed timestamp.
	// Refer to stream timestamping configuration for the finer semantics around whether a client-specified timestamp is required,
	// and whether it will be capped at the arrival time.
	Timestamp *uint64 `json:"timestamp,omitempty"`
	// Series of name-value pairs for this record.
	Headers []Header `json:"headers,omitempty"`
	// Body of the record.
	Body []byte `json:"body,omitempty"`
}

// Payload of an `append` request.
type AppendInput struct {
	// Batch of records to append atomically, which must contain at least one record, and no more than 1000.
	// The total size of a batch of records may not exceed 1 MiB of metered bytes.
	Records []AppendRecord `json:"records"`
	// Enforce that the sequence number assigned to the first record matches.
	MatchSeqNum *uint64 `json:"match_seq_num,omitempty"`
	// Enforce a fencing token, which starts out as an empty string that can be overridden by a `fence` command record.
	FencingToken *string `json:"fencing_token,omitempty"`
}

// Success response to an `append` request.
type AppendAck struct {
	// Sequence number and timestamp of the first record that was appended.
	Start StreamPosition `json:"start"`
	// Sequence number of the last record that was appended `+ 1`, and timestamp of the last record that was appended.
	// The difference between `end.seq_num` and `start.seq_num` will be the number of records appended.
	End StreamPosition `json:"end"`
	// Sequence number that will be assigned to the next record on the stream, and timestamp of the last record on the stream.
	// This can be greater than the `end` position in case of concurrent appends.
	Tail StreamPosition `json:"tail"`
}

type BatchingOptions struct {
	// Duration to wait before flushing a batch (default: 5ms)
	Linger time.Duration
	// Maximum number of records in a batch (default: 1000, max: 1000)
	MaxRecords int
	// Maximum batch size in metered bytes (default: 1 MiB, max: 1 MiB)
	MaxMeteredBytes uint64
	// Optional sequence number to match for first batch (auto-increments for subsequent batches)
	MatchSeqNum *uint64
	// Optional fencing token to enforce (remains static across batches)
	FencingToken *string
	// Buffer size for the internal batches channel (default: 16)
	ChannelBuffer int
}

type AppendSessionOptions struct {
	// Aggregate size of records, to allow in-flight before applying backpressure (default: 10 MiB).
	MaxInflightBytes uint64
	// Maximum number of batches allowed in-flight before applying backpressure.
	MaxInflightBatches uint32
	// Retry configuration for handling transient failures.
	// Applies to management operations (basins, streams, tokens) and stream operations (read, append).
	RetryConfig *RetryConfig
}

type ListAccessTokensArgs struct {
	// Filter to access tokens whose ID begins with this prefix.
	Prefix string `json:"prefix,omitempty"`
	// Filter to access tokens whose ID lexicographically starts after this string.
	StartAfter string `json:"start_after,omitempty"`
	// Number of results, up to a maximum of 1000.
	Limit *int `json:"limit,omitempty"`
}

type IssueAccessTokenArgs struct {
	// Access token ID.
	// It must be unique to the account and between 1 and 96 bytes in length.
	ID AccessTokenID `json:"id"`
	// Access token scope.
	Scope AccessTokenScope `json:"scope"`
	// Namespace streams based on the configured stream-level scope, which must be a prefix.
	// Stream name arguments will be automatically prefixed, and the prefix will be stripped when listing streams.
	AutoPrefixStreams bool `json:"auto_prefix_streams,omitempty"`
	// Expiration time. If not set, the expiration will be set to that of the requestor's token.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type RevokeAccessTokenArgs struct {
	// Access token ID.
	ID AccessTokenID `json:"id"`
}

type ListBasinsArgs struct {
	// Filter to basins whose names begin with this prefix.
	Prefix string `json:"prefix,omitempty"`
	// Filter to basins whose names lexicographically start after this string.
	// It must be greater than or equal to the `prefix` if specified.
	StartAfter string `json:"start_after,omitempty"`
	// Number of results, up to a maximum of 1000.
	Limit *int `json:"limit,omitempty"`
}

type CreateBasinArgs struct {
	// Basin name which must be globally unique.
	// It can be between 8 and 48 characters in length, and comprise lowercase letters, numbers and hyphens.
	// It cannot begin or end with a hyphen.
	Basin BasinName `json:"basin"`
	// Basin configuration.
	Config *BasinConfig `json:"config,omitempty"`
	// Basin scope.
	Scope *BasinScope `json:"scope,omitempty"`
}

type ReconfigureBasinArgs struct {
	// Basin name.
	Basin BasinName
	// Basin configuration.
	Config BasinReconfiguration
}

type ListStreamsArgs struct {
	// Filter to streams whose name begins with this prefix.
	Prefix string `json:"prefix,omitempty"`
	// Filter to streams whose name begins with this prefix.
	// It must be greater than or equal to the `prefix` if specified.
	StartAfter string `json:"start_after,omitempty"`
	// Number of results, up to a maximum of 1000.
	Limit *int `json:"limit,omitempty"`
}

type CreateStreamArgs struct {
	// Stream name.
	Stream StreamName `json:"stream"`
	// Stream configuration.
	Config *StreamConfig `json:"config,omitempty"`
}

type ReconfigureStreamArgs struct {
	// Stream name.
	Stream StreamName
	// Stream configuration.
	Config StreamReconfiguration
}
