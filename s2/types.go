package s2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/internal/pb"
)

const (
	mibBytes uint = 1024 * 1024

	// Maximum metered bytes of a record.
	MaxRecordBytes = mibBytes
	// Maximum metered bytes of a batch.
	MaxBatchBytes = mibBytes
	// Maximum number of records that a batch can hold.
	MaxBatchRecords = 1000

	unspecifiedEnumVariant = "unspecified"
)

// Type conversion errors.
var (
	ErrUnknownBasinState       = errors.New("unknown basin state")
	ErrUnknownBasinScope       = errors.New("unknown basin scope")
	ErrUnknownStorageClass     = errors.New("unknown storage class")
	ErrUnknownTimestampingMode = errors.New("unknown timestamping mode")
	ErrUnknownRetentionPolicy  = errors.New("unknown retention policy")
	ErrUnknownReadOutput       = errors.New("unknown read output")
	ErrUnknownOperation        = errors.New("unknown operation")
	ErrUnknownResourceSet      = errors.New("unknown resource set")

	// Sentinel error to signify a heartbeat message.
	errHeartbeatMessage = errors.New("heartbeat")
)

// Metered size of the object in bytes.
//
// Bytes are calculated using the “metered bytes” formula:
//
//	metered_bytes = lambda record: 8 + 2 * len(record.headers) \
//	 + sum((len(h.key) + len(h.value)) for h in record.headers) \
//	 + len(record.body)
type MeteredBytes interface {
	MeteredBytes() uint
}

// Metered bytes for an AppendRecord.
func (r *AppendRecord) MeteredBytes() uint {
	bytes := 8 + (2 * uint(len(r.Headers))) + uint(len(r.Body))
	for _, header := range r.Headers {
		bytes += uint(len(header.Name)) + uint(len(header.Value))
	}

	return bytes
}

// Metered bytes for a SequencedRecord.
func (r *SequencedRecord) MeteredBytes() uint {
	bytes := 8 + (2 * uint(len(r.Headers))) + uint(len(r.Body))
	for _, header := range r.Headers {
		bytes += uint(len(header.Name)) + uint(len(header.Value))
	}

	return bytes
}

// Metered bytes for a SequencedRecordBatch.
func (b *ReadOutputBatch) MeteredBytes() uint {
	var bytes uint
	for i := range len(b.Records) {
		bytes += b.Records[i].MeteredBytes()
	}

	return bytes
}

// A collection of append records that can be sent together in a batch.
type AppendRecordBatch struct {
	records      []AppendRecord
	meteredBytes uint
	maxCapacity  uint
	maxBytes     uint
}

func newAppendRecordBatch(maxCapacity, maxBytes uint, records ...AppendRecord) (*AppendRecordBatch, []AppendRecord) {
	var (
		i            uint
		meteredBytes uint
	)

	for range uint(len(records)) {
		recordBytes := records[i].MeteredBytes()

		if i >= maxCapacity || meteredBytes+recordBytes > maxBytes {
			break
		}

		i++
		meteredBytes += recordBytes
	}

	return &AppendRecordBatch{
		records:      records[:i],
		meteredBytes: meteredBytes,
		maxCapacity:  maxCapacity,
		maxBytes:     maxBytes,
	}, records[i:]
}

func newEmptyAppendRecordBatch(maxCapacity, maxBytes uint) *AppendRecordBatch {
	batch, leftOver := newAppendRecordBatch(maxCapacity, maxBytes)
	if len(leftOver) != 0 {
		panic("empty append record batch should not have any left-overs")
	}

	return batch
}

// Try creating a record batch from records.
//
// If all the items of the iterator cannot be drained into the batch, a non-empty slice of records is returned along
// with the batch containing all the records it could fit.
//
//	batch, leftOver := NewAppendRecordBatch(records...)
//	batches := []*AppendRecordBatch{batch}
//	for len(leftOver) > 0 {
//		batch, leftOver = NewAppendRecordBatch(leftOver...)
//		batches = append(batches, batch)
//	}
func NewAppendRecordBatch(records ...AppendRecord) (*AppendRecordBatch, []AppendRecord) {
	return newAppendRecordBatch(MaxBatchRecords, MaxBatchBytes, records...)
}

// Try creating a record batch with custom max capacity from records.
//
// See NewAppendRecordBatch for more details.
func NewAppendRecordBatchWithMaxCapacity(
	maxCapacity uint,
	records ...AppendRecord,
) (*AppendRecordBatch, []AppendRecord) {
	return newAppendRecordBatch(maxCapacity, MaxBatchBytes, records...)
}

// Try appending a record in the batch. Returns false if we cannot append the record.
func (b *AppendRecordBatch) Append(record AppendRecord) bool {
	recordBytes := record.MeteredBytes()
	if uint(len(b.records)) == b.maxCapacity || b.meteredBytes+recordBytes > b.maxBytes {
		return false
	}

	b.records = append(b.records, record)
	b.meteredBytes += recordBytes

	return true
}

// Number of records in the batch.
func (b *AppendRecordBatch) Len() uint {
	return uint(len(b.records))
}

// Returns true if there are no records in the batch.
func (b *AppendRecordBatch) IsEmpty() bool {
	return b.Len() == 0
}

// Returns true if the batch is at its maximum capacity.
func (b *AppendRecordBatch) IsFull() bool {
	return uint(len(b.records)) == b.maxCapacity || b.meteredBytes == b.maxBytes
}

// Returns the records stored in the batch.
func (b *AppendRecordBatch) Records() []AppendRecord {
	return b.records
}

// Returns metered bytes for the batch.
func (b *AppendRecordBatch) MeteredBytes() uint {
	return b.meteredBytes
}

// A command record is a special kind of AppendRecord that can be used to send command messages.
//
// Such a record is signalled by a sole header with empty name. The header value represents the operation and record
// body acts as the payload.
//
// Valid CommandRecord variants are:
//   - CommandRecordFence
//   - CommandRecordTrim
type CommandRecord interface {
	commandRecordParts() (string, []byte)
}

// Enforce a fencing token.
//
// Fencing is strongly consistent, and subsequent appends that specify a fencing token will be rejected if it
// does not match.
type CommandRecordFence struct {
	// Fencing token to enforce.
	//
	// Set empty to clear the token.
	FencingToken []byte
}

func (c CommandRecordFence) commandRecordParts() (string, []byte) {
	return "fence", c.FencingToken
}

// Request a trim till the sequence number.
//
// Trimming is eventually consistent, and trimmed records may be visible for a brief period.
type CommandRecordTrim struct {
	// Trim point.
	//
	// This sequence number is only allowed to advance, and any regression will be ignored.
	SeqNum uint64
}

func (c CommandRecordTrim) commandRecordParts() (string, []byte) {
	seqNum := make([]byte, 0, 8)
	seqNum = binary.BigEndian.AppendUint64(seqNum, c.SeqNum)

	return "trim", seqNum
}

func AppendRecordFromCommand(c CommandRecord) AppendRecord {
	headerVal, body := c.commandRecordParts()

	return AppendRecord{
		Headers: []Header{{Value: []byte(headerVal)}},
		Body:    body,
	}
}

// Generate a random fencing token.
//
// Panics if n > 16.
func GenerateFencingToken(n uint8) []byte {
	if n > 16 {
		panic("fencing token cannot be > 16 bytes")
	}

	fencingToken := make([]byte, 16)
	for i := range fencingToken {
		fencingToken[i] = byte(rand.UintN(256))
	}

	return fencingToken
}

// A listener on streaming responses for next item.
type Receiver[T any] interface {
	// Block until there's another item available or error response.
	Recv() (T, error)
}

// An item sender for streaming requests.
type Sender[T any] interface {
	// Block until the item has been sent.
	Send(T) error
	// Close the sender.
	Close() error
}

type recvInner[F, T any] struct {
	Client interface {
		Recv() (*F, error)
	}
	ConvertFn func(*F) (T, error)
}

func (r recvInner[F, T]) Recv() (T, error) {
	f, err := r.Client.Recv()
	if err != nil {
		var v T

		return v, err
	}

	return r.ConvertFn(f)
}

type sendInner[F, T any] struct {
	Client interface {
		Send(*T) error
		CloseSend() error
	}
	ConvertFn func(F) (*T, error)
}

func (r sendInner[F, T]) Send(f F) error {
	t, err := r.ConvertFn(f)
	if err != nil {
		return err
	}

	return r.Client.Send(t)
}

func (r sendInner[F, T]) Close() error {
	return r.Client.CloseSend()
}

func (a RetentionPolicyAge) implRetentionPolicy() {}

func (s StorageClass) String() string {
	switch s {
	case StorageClassUnspecified:
		return unspecifiedEnumVariant
	case StorageClassStandard:
		return "standard"
	case StorageClassExpress:
		return "express"
	default:
		return "<unknown storage class>"
	}
}

func (s BasinScope) String() string {
	switch s {
	case BasinScopeUnspecified:
		return unspecifiedEnumVariant
	case BasinScopeAwsUSEast1:
		return "aws:us-east-1"
	default:
		return "<unknown basin scope>"
	}
}

func (r ResourceSetExact) implResourceSet() {}

func (r ResourceSetExact) String() string {
	return fmt.Sprintf("Exact(%s)", string(r))
}

func (r ResourceSetPrefix) implResourceSet() {}

func (r ResourceSetPrefix) String() string {
	return fmt.Sprintf("Prefix(%s)", string(r))
}

func basinScopeFromProto(pbScope pb.BasinScope) (BasinScope, error) {
	switch pbScope {
	case pb.BasinScope_BASIN_SCOPE_UNSPECIFIED:
		return BasinScopeUnspecified, nil
	case pb.BasinScope_BASIN_SCOPE_AWS_US_EAST_1:
		return BasinScopeAwsUSEast1, nil
	default:
		return 0, fmt.Errorf("%w: %d", ErrUnknownBasinScope, pbScope)
	}
}

func basinScopeIntoProto(scope BasinScope) (pb.BasinScope, error) {
	switch scope {
	case BasinScopeUnspecified:
		return pb.BasinScope_BASIN_SCOPE_UNSPECIFIED, nil
	case BasinScopeAwsUSEast1:
		return pb.BasinScope_BASIN_SCOPE_AWS_US_EAST_1, nil
	default:
		return 0, fmt.Errorf("%w: %d", ErrUnknownBasinScope, scope)
	}
}

func (s BasinState) String() string {
	switch s {
	case BasinStateUnspecified:
		return unspecifiedEnumVariant
	case BasinStateActive:
		return "active"
	case BasinStateCreating:
		return "creating"
	case BasinStateDeleting:
		return "deleting"
	default:
		return "<unknown basin state>"
	}
}

func basinInfoFromProto(pbInfo *pb.BasinInfo) (BasinInfo, error) {
	var state BasinState

	switch pbInfo.GetState() {
	case pb.BasinState_BASIN_STATE_UNSPECIFIED:
		state = BasinStateUnspecified
	case pb.BasinState_BASIN_STATE_ACTIVE:
		state = BasinStateActive
	case pb.BasinState_BASIN_STATE_CREATING:
		state = BasinStateCreating
	case pb.BasinState_BASIN_STATE_DELETING:
		state = BasinStateCreating
	default:
		return BasinInfo{}, fmt.Errorf("%w: %d", ErrUnknownBasinState, pbInfo.GetState())
	}

	scope, err := basinScopeFromProto(pbInfo.GetScope())
	if err != nil {
		return BasinInfo{}, err
	}

	return BasinInfo{
		Name:  pbInfo.GetName(),
		Scope: scope,
		State: state,
	}, nil
}

func streamInfoFromProto(pbInfo *pb.StreamInfo) StreamInfo {
	deletedAt := optr.Map(pbInfo.DeletedAt, func(timestamp uint32) time.Time {
		return time.Unix(int64(timestamp), 0)
	})

	return StreamInfo{
		Name:      pbInfo.GetName(),
		CreatedAt: time.Unix(int64(pbInfo.GetCreatedAt()), 0),
		DeletedAt: deletedAt,
	}
}

func streamConfigFromProto(pbConfig *pb.StreamConfig) (*StreamConfig, error) {
	var storageClass StorageClass

	switch pbConfig.GetStorageClass() {
	case pb.StorageClass_STORAGE_CLASS_UNSPECIFIED:
		storageClass = StorageClassUnspecified
	case pb.StorageClass_STORAGE_CLASS_STANDARD:
		storageClass = StorageClassStandard
	case pb.StorageClass_STORAGE_CLASS_EXPRESS:
		storageClass = StorageClassExpress
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownStorageClass, pbConfig.GetStorageClass())
	}

	var retentionPolicy RetentionPolicy
	switch r := pbConfig.GetRetentionPolicy().(type) {
	case *pb.StreamConfig_Age:
		retentionPolicy = RetentionPolicyAge(r.Age * uint64(time.Second))
	case nil:
		retentionPolicy = nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownRetentionPolicy, r)
	}

	pbTimestamping := pbConfig.GetTimestamping()

	var timestampingMode TimestampingMode

	switch pbTimestamping.GetMode() {
	case pb.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED:
		timestampingMode = TimestampingModeUnspecified
	case pb.TimestampingMode_TIMESTAMPING_MODE_CLIENT_PREFER:
		timestampingMode = TimestampingModeClientPrefer
	case pb.TimestampingMode_TIMESTAMPING_MODE_CLIENT_REQUIRE:
		timestampingMode = TimestampingModeClientRequire
	case pb.TimestampingMode_TIMESTAMPING_MODE_ARRIVAL:
		timestampingMode = TimestampingModeClientArrival
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownTimestampingMode, pbTimestamping.GetMode())
	}

	timestamping := &Timestamping{
		Mode:     timestampingMode,
		Uncapped: pbTimestamping.Uncapped,
	}

	return &StreamConfig{
		StorageClass:    storageClass,
		RetentionPolicy: retentionPolicy,
		Timestamping:    timestamping,
	}, nil
}

func streamConfigIntoProto(config *StreamConfig) (*pb.StreamConfig, error) {
	pbConfig := new(pb.StreamConfig)

	switch config.StorageClass {
	case StorageClassUnspecified:
		pbConfig.StorageClass = pb.StorageClass_STORAGE_CLASS_UNSPECIFIED
	case StorageClassStandard:
		pbConfig.StorageClass = pb.StorageClass_STORAGE_CLASS_STANDARD
	case StorageClassExpress:
		pbConfig.StorageClass = pb.StorageClass_STORAGE_CLASS_EXPRESS
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownStorageClass, config.StorageClass)
	}

	switch r := config.RetentionPolicy.(type) {
	case RetentionPolicyAge:
		pbConfig.RetentionPolicy = &pb.StreamConfig_Age{Age: uint64(time.Duration(r) / time.Second)}
	case nil:
		pbConfig.RetentionPolicy = nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownRetentionPolicy, r)
	}

	var timestampingMode pb.TimestampingMode

	switch config.Timestamping.Mode {
	case TimestampingModeUnspecified:
		timestampingMode = pb.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED
	case TimestampingModeClientPrefer:
		timestampingMode = pb.TimestampingMode_TIMESTAMPING_MODE_CLIENT_PREFER
	case TimestampingModeClientRequire:
		timestampingMode = pb.TimestampingMode_TIMESTAMPING_MODE_CLIENT_REQUIRE
	case TimestampingModeClientArrival:
		timestampingMode = pb.TimestampingMode_TIMESTAMPING_MODE_ARRIVAL
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownTimestampingMode, config.Timestamping.Mode)
	}

	pbConfig.Timestamping = &pb.StreamConfig_Timestamping{
		Mode:     timestampingMode,
		Uncapped: config.Timestamping.Uncapped,
	}

	return pbConfig, nil
}

func basinConfigFromProto(pbConfig *pb.BasinConfig) (*BasinConfig, error) {
	var defaultStreamConfig *StreamConfig

	pbDefaultStreamConfig := pbConfig.GetDefaultStreamConfig()
	if pbDefaultStreamConfig != nil {
		var err error

		defaultStreamConfig, err = streamConfigFromProto(pbDefaultStreamConfig)
		if err != nil {
			return nil, err
		}
	}

	return &BasinConfig{
		DefaultStreamConfig:  defaultStreamConfig,
		CreateStreamOnAppend: pbConfig.GetCreateStreamOnAppend(),
		CreateStreamOnRead:   pbConfig.GetCreateStreamOnRead(),
	}, nil
}

func basinConfigIntoProto(config *BasinConfig) (*pb.BasinConfig, error) {
	pbConfig := new(pb.BasinConfig)

	if config.DefaultStreamConfig != nil {
		var err error

		pbConfig.DefaultStreamConfig, err = streamConfigIntoProto(config.DefaultStreamConfig)
		if err != nil {
			return nil, err
		}
	}

	pbConfig.CreateStreamOnAppend = config.CreateStreamOnAppend
	pbConfig.CreateStreamOnRead = config.CreateStreamOnRead

	return pbConfig, nil
}

func (ReadOutputBatch) implReadOutput()      {}
func (ReadOutputNextSeqNum) implReadOutput() {}

func (ReadStartSeqNum) implReadStart() {}

func (r ReadStartSeqNum) String() string {
	return fmt.Sprintf("SeqNum(%d)", uint64(r))
}

func (ReadStartTimestamp) implReadStart() {}

func (r ReadStartTimestamp) String() string {
	return fmt.Sprintf("Timestamp(%d)", uint64(r))
}

func (ReadStartTailOffset) implReadStart() {}

func (r ReadStartTailOffset) String() string {
	return fmt.Sprintf("TailOffset(%d)", uint64(r))
}

func headerFromProto(pbHeader *pb.Header) Header {
	return Header{
		Name:  pbHeader.GetName(),
		Value: pbHeader.GetValue(),
	}
}

func sequencedRecordFromProto(pbRecord *pb.SequencedRecord) SequencedRecord {
	pbHeaders := pbRecord.GetHeaders()
	headers := make([]Header, 0, len(pbHeaders))

	for _, h := range pbHeaders {
		headers = append(headers, headerFromProto(h))
	}

	return SequencedRecord{
		SeqNum:    pbRecord.GetSeqNum(),
		Timestamp: pbRecord.GetTimestamp(),
		Headers:   headers,
		Body:      pbRecord.GetBody(),
	}
}

func sequencedRecordBatchFromProto(pbBatch *pb.SequencedRecordBatch) []SequencedRecord {
	pbRecords := pbBatch.GetRecords()
	records := make([]SequencedRecord, 0, len(pbRecords))

	for _, r := range pbRecords {
		records = append(records, sequencedRecordFromProto(r))
	}

	return records
}

func readOutputFromProto(pbOutput *pb.ReadOutput, acceptHeartbeats bool) (ReadOutput, error) {
	if acceptHeartbeats && pbOutput.GetOutput() == nil {
		// Heartbeat message.
		return nil, errHeartbeatMessage
	}

	var output ReadOutput
	switch o := pbOutput.GetOutput().(type) {
	case *pb.ReadOutput_Batch:
		output = ReadOutputBatch{
			Records: sequencedRecordBatchFromProto(o.Batch),
		}
	case *pb.ReadOutput_NextSeqNum:
		output = ReadOutputNextSeqNum(o.NextSeqNum)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownReadOutput, o)
	}

	return output, nil
}

func headerIntoProto(header Header) *pb.Header {
	return &pb.Header{
		Name:  header.Name,
		Value: header.Value,
	}
}

func appendRecordIntoProto(record *AppendRecord) *pb.AppendRecord {
	headers := make([]*pb.Header, 0, len(record.Headers))
	for _, h := range record.Headers {
		headers = append(headers, headerIntoProto(h))
	}

	return &pb.AppendRecord{
		Timestamp: record.Timestamp,
		Headers:   headers,
		Body:      record.Body,
	}
}

func appendInputIntoProto(stream string, input *AppendInput) *pb.AppendInput {
	inputRecords := input.Records.Records()
	records := make([]*pb.AppendRecord, 0, len(inputRecords))

	for i := range inputRecords {
		records = append(records, appendRecordIntoProto(&inputRecords[i]))
	}

	return &pb.AppendInput{
		Stream:       stream,
		Records:      records,
		MatchSeqNum:  input.MatchSeqNum,
		FencingToken: input.FencingToken,
	}
}

func appendOutputFromProto(pbOutput *pb.AppendOutput) *AppendOutput {
	return &AppendOutput{
		StartSeqNum:    pbOutput.GetStartSeqNum(),
		StartTimestamp: pbOutput.GetStartTimestamp(),
		EndSeqNum:      pbOutput.GetEndSeqNum(),
		EndTimestamp:   pbOutput.GetEndTimestamp(),
		NextSeqNum:     pbOutput.GetNextSeqNum(),
		LastTimestamp:  pbOutput.GetLastTimestamp(),
	}
}

func operationFromProto(pbOp pb.Operation) (Operation, error) {
	switch pbOp {
	case pb.Operation_OPERATION_UNSPECIFIED:
		return OperationUnspecified, nil
	case pb.Operation_OPERATION_LIST_BASINS:
		return OperationListBasins, nil
	case pb.Operation_OPERATION_CREATE_BASIN:
		return OperationCreateBasin, nil
	case pb.Operation_OPERATION_DELETE_BASIN:
		return OperationDeleteBasin, nil
	case pb.Operation_OPERATION_RECONFIGURE_BASIN:
		return OperationReconfigureBasin, nil
	case pb.Operation_OPERATION_GET_BASIN_CONFIG:
		return OperationGetBasinConfig, nil
	case pb.Operation_OPERATION_ISSUE_ACCESS_TOKEN:
		return OperationIssueAccessToken, nil
	case pb.Operation_OPERATION_REVOKE_ACCESS_TOKEN:
		return OperationRevokeAccessToken, nil
	case pb.Operation_OPERATION_LIST_ACCESS_TOKENS:
		return OperationListAccessTokens, nil
	case pb.Operation_OPERATION_LIST_STREAMS:
		return OperationListStreams, nil
	case pb.Operation_OPERATION_CREATE_STREAM:
		return OperationCreateStream, nil
	case pb.Operation_OPERATION_DELETE_STREAM:
		return OperationDeleteStream, nil
	case pb.Operation_OPERATION_GET_STREAM_CONFIG:
		return OperationGetStreamConfig, nil
	case pb.Operation_OPERATION_RECONFIGURE_STREAM:
		return OperationReconfigureStream, nil
	case pb.Operation_OPERATION_CHECK_TAIL:
		return OperationCheckTail, nil
	case pb.Operation_OPERATION_APPEND:
		return OperationAppend, nil
	case pb.Operation_OPERATION_READ:
		return OperationRead, nil
	case pb.Operation_OPERATION_TRIM:
		return OperationTrim, nil
	case pb.Operation_OPERATION_FENCE:
		return OperationFence, nil
	case pb.Operation_OPERATION_ACCOUNT_METRICS:
		return OperationAccountMetrics, nil
	case pb.Operation_OPERATION_BASIN_METRICS:
		return OperationAccountMetrics, nil
	case pb.Operation_OPERATION_STREAM_METRICS:
		return OperationAccountMetrics, nil
	default:
		return OperationUnspecified, fmt.Errorf("%w: %v", ErrUnknownOperation, pbOp)
	}
}

func operationsFromProto(pbOps []pb.Operation) ([]Operation, error) {
	ops := make([]Operation, 0, len(pbOps))

	for _, pbOp := range pbOps {
		op, err := operationFromProto(pbOp)
		if err != nil {
			return nil, err
		}

		ops = append(ops, op)
	}

	return ops, nil
}

func resourceSetFromProto(pbSet *pb.ResourceSet) (ResourceSet, error) {
	if pbSet == nil || pbSet.Matching == nil {
		// Most restrictive when nil.
		return ResourceSetExact(""), nil
	}

	switch matching := pbSet.Matching.(type) {
	case *pb.ResourceSet_Exact:
		return ResourceSetExact(matching.Exact), nil
	case *pb.ResourceSet_Prefix:
		return ResourceSetPrefix(matching.Prefix), nil
	default:
		return nil, fmt.Errorf("%w: %v", ErrUnknownResourceSet, matching)
	}
}

func readWritePermissionsFromProto(pbPerms *pb.ReadWritePermissions) *ReadWritePermissions {
	return &ReadWritePermissions{
		Read:  pbPerms.Read,
		Write: pbPerms.Write,
	}
}

func permittedOperationGroupsFromProto(pbGroups *pb.PermittedOperationGroups) *PermittedOperationGroups {
	return &PermittedOperationGroups{
		Account: readWritePermissionsFromProto(pbGroups.Account),
		Basin:   readWritePermissionsFromProto(pbGroups.Basin),
		Stream:  readWritePermissionsFromProto(pbGroups.Stream),
	}
}

func accessTokenScopeFromProto(pbScope *pb.AccessTokenScope) (*AccessTokenScope, error) {
	ops, err := operationsFromProto(pbScope.GetOps())
	if err != nil {
		return nil, err
	}

	basins, err := resourceSetFromProto(pbScope.GetBasins())
	if err != nil {
		return nil, err
	}

	streams, err := resourceSetFromProto(pbScope.GetStreams())
	if err != nil {
		return nil, err
	}

	accessTokens, err := resourceSetFromProto(pbScope.GetAccessTokens())
	if err != nil {
		return nil, err
	}

	return &AccessTokenScope{
		Basins:       basins,
		Streams:      streams,
		AccessTokens: accessTokens,
		OpGroups:     permittedOperationGroupsFromProto(pbScope.GetOpGroups()),
		Ops:          ops,
	}, nil
}

func accessTokenInfoFromProto(pbInfo *pb.AccessTokenInfo) (*AccessTokenInfo, error) {
	scope, err := accessTokenScopeFromProto(pbInfo.GetScope())
	if err != nil {
		return nil, err
	}

	expiresAt := time.Unix(int64(pbInfo.GetExpiresAt()), 0)

	return &AccessTokenInfo{
		ID:                pbInfo.GetId(),
		ExpiresAt:         optr.Some(expiresAt),
		AutoPrefixStreams: pbInfo.GetAutoPrefixStreams(),
		Scope:             scope,
	}, nil
}

func operationToProto(op Operation) (pb.Operation, error) {
	switch op {
	case OperationUnspecified:
		return pb.Operation_OPERATION_UNSPECIFIED, nil
	case OperationListBasins:
		return pb.Operation_OPERATION_LIST_BASINS, nil
	case OperationCreateBasin:
		return pb.Operation_OPERATION_CREATE_BASIN, nil
	case OperationDeleteBasin:
		return pb.Operation_OPERATION_DELETE_BASIN, nil
	case OperationReconfigureBasin:
		return pb.Operation_OPERATION_RECONFIGURE_BASIN, nil
	case OperationGetBasinConfig:
		return pb.Operation_OPERATION_GET_BASIN_CONFIG, nil
	case OperationIssueAccessToken:
		return pb.Operation_OPERATION_ISSUE_ACCESS_TOKEN, nil
	case OperationRevokeAccessToken:
		return pb.Operation_OPERATION_REVOKE_ACCESS_TOKEN, nil
	case OperationListAccessTokens:
		return pb.Operation_OPERATION_LIST_ACCESS_TOKENS, nil
	case OperationListStreams:
		return pb.Operation_OPERATION_LIST_STREAMS, nil
	case OperationCreateStream:
		return pb.Operation_OPERATION_CREATE_STREAM, nil
	case OperationDeleteStream:
		return pb.Operation_OPERATION_DELETE_STREAM, nil
	case OperationGetStreamConfig:
		return pb.Operation_OPERATION_GET_STREAM_CONFIG, nil
	case OperationReconfigureStream:
		return pb.Operation_OPERATION_RECONFIGURE_STREAM, nil
	case OperationCheckTail:
		return pb.Operation_OPERATION_CHECK_TAIL, nil
	case OperationAppend:
		return pb.Operation_OPERATION_APPEND, nil
	case OperationRead:
		return pb.Operation_OPERATION_READ, nil
	case OperationTrim:
		return pb.Operation_OPERATION_TRIM, nil
	case OperationFence:
		return pb.Operation_OPERATION_FENCE, nil
	default:
		return pb.Operation_OPERATION_UNSPECIFIED, fmt.Errorf("%w: %v", ErrUnknownOperation, op)
	}
}

func operationsToProto(ops []Operation) ([]pb.Operation, error) {
	pbOps := make([]pb.Operation, 0, len(ops))

	for _, op := range ops {
		pbOp, err := operationToProto(op)
		if err != nil {
			return nil, err
		}

		pbOps = append(pbOps, pbOp)
	}

	return pbOps, nil
}

func resourceSetToProto(rs ResourceSet) (*pb.ResourceSet, error) {
	if rs == nil {
		return &pb.ResourceSet{Matching: &pb.ResourceSet_Exact{Exact: ""}}, nil
	}

	switch v := rs.(type) {
	case ResourceSetExact:
		return &pb.ResourceSet{
			Matching: &pb.ResourceSet_Exact{Exact: string(v)},
		}, nil
	case ResourceSetPrefix:
		return &pb.ResourceSet{
			Matching: &pb.ResourceSet_Prefix{Prefix: string(v)},
		}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownResourceSet, v)
	}
}

func readWritePermissionsToProto(p *ReadWritePermissions) *pb.ReadWritePermissions {
	if p == nil {
		return nil
	}

	return &pb.ReadWritePermissions{
		Read:  p.Read,
		Write: p.Write,
	}
}

func permittedOperationGroupsToProto(p *PermittedOperationGroups) *pb.PermittedOperationGroups {
	if p == nil {
		return nil
	}

	return &pb.PermittedOperationGroups{
		Account: readWritePermissionsToProto(p.Account),
		Basin:   readWritePermissionsToProto(p.Basin),
		Stream:  readWritePermissionsToProto(p.Stream),
	}
}

func accessTokenScopeToProto(s *AccessTokenScope) (*pb.AccessTokenScope, error) {
	pbOps, err := operationsToProto(s.Ops)
	if err != nil {
		return nil, err
	}

	pbBasins, err := resourceSetToProto(s.Basins)
	if err != nil {
		return nil, err
	}

	pbStreams, err := resourceSetToProto(s.Streams)
	if err != nil {
		return nil, err
	}

	pbAccessTokens, err := resourceSetToProto(s.AccessTokens)
	if err != nil {
		return nil, err
	}

	return &pb.AccessTokenScope{
		Basins:       pbBasins,
		Streams:      pbStreams,
		AccessTokens: pbAccessTokens,
		OpGroups:     permittedOperationGroupsToProto(s.OpGroups),
		Ops:          pbOps,
	}, nil
}

func accessTokenInfoToProto(i *AccessTokenInfo) (*pb.AccessTokenInfo, error) {
	scope, err := accessTokenScopeToProto(i.Scope)
	if err != nil {
		return nil, err
	}

	return &pb.AccessTokenInfo{
		Id: i.ID,
		ExpiresAt: optr.Map(i.ExpiresAt, func(t time.Time) uint32 {
			return uint32(t.Unix())
		}),
		AutoPrefixStreams: i.AutoPrefixStreams,
		Scope:             scope,
	}, nil
}
