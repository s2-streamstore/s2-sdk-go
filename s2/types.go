package s2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

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
)

// Type conversion errors.
var (
	ErrUnknownBasinState      = errors.New("unknown basin state")
	ErrUnknownStorageClass    = errors.New("unknown storage class")
	ErrUnknownRetentionPolicy = errors.New("unknown retention policy")
	ErrUnknownReadOutput      = errors.New("unknown read output")
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

	for i = range uint(len(records)) {
		recordBytes := records[i].MeteredBytes()

		if i >= maxCapacity || meteredBytes+recordBytes > maxBytes {
			break
		}

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
}

type recvInner[F, T any] struct {
	Client    Receiver[*F]
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
	Client    Sender[*T]
	ConvertFn func(F) (*T, error)
}

func (r sendInner[F, T]) Send(f F) error {
	t, err := r.ConvertFn(f)
	if err != nil {
		return err
	}

	return r.Client.Send(t)
}

type implRetentionPolicy interface {
	implRetentionPolicy()
}

func (a RetentionPolicyAge) implRetentionPolicy() {}

func (s StorageClass) String() string {
	switch s {
	case StorageClassUnspecified:
		return "unspecified"
	case StorageClassStandard:
		return "standard"
	case StorageClassExpress:
		return "express"
	default:
		return "<unknown storage class>"
	}
}

func (s BasinState) String() string {
	switch s {
	case BasinStateUnspecified:
		return "unspecified"
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

	return BasinInfo{
		Name:  pbInfo.GetName(),
		Scope: pbInfo.GetScope(),
		Cell:  pbInfo.GetCell(),
		State: state,
	}, nil
}

func streamInfoFromProto(pbInfo *pb.StreamInfo) StreamInfo {
	var deletedAt *time.Time

	if pbInfo.DeletedAt != nil {
		deletedAtTime := time.Unix(int64(pbInfo.GetDeletedAt()), 0)
		deletedAt = &deletedAtTime
	}

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

	var retentionPolicy implRetentionPolicy
	switch r := pbConfig.GetRetentionPolicy().(type) {
	case *pb.StreamConfig_Age:
		retentionPolicy = RetentionPolicyAge(r.Age * uint64(time.Second))
	case nil:
		retentionPolicy = nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnknownRetentionPolicy, r)
	}

	return &StreamConfig{
		StorageClass:    storageClass,
		RetentionPolicy: retentionPolicy,
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
		DefaultStreamConfig: defaultStreamConfig,
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

	return pbConfig, nil
}

func (b ReadOutputBatch) implReadOutput()       {}
func (f ReadOutputFirstSeqNum) implReadOutput() {}
func (n ReadOutputNextSeqNum) implReadOutput()  {}

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
		SeqNum:  pbRecord.GetSeqNum(),
		Headers: headers,
		Body:    pbRecord.GetBody(),
	}
}

func sequencedRecordBatchFromProto(pbBatch *pb.SequencedRecordBatch) *SequencedRecordBatch {
	pbRecords := pbBatch.GetRecords()
	records := make([]SequencedRecord, 0, len(pbRecords))

	for _, r := range pbRecords {
		records = append(records, sequencedRecordFromProto(r))
	}

	return &SequencedRecordBatch{
		Records: records,
	}
}

func readOutputFromProto(pbOutput *pb.ReadOutput) (ReadOutput, error) {
	var output ReadOutput
	switch o := pbOutput.GetOutput().(type) {
	case *pb.ReadOutput_Batch:
		output = ReadOutputBatch{
			SequencedRecordBatch: sequencedRecordBatchFromProto(o.Batch),
		}
	case *pb.ReadOutput_FirstSeqNum:
		output = ReadOutputFirstSeqNum(o.FirstSeqNum)
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
		Headers: headers,
		Body:    record.Body,
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
		StartSeqNum: pbOutput.GetStartSeqNum(),
		NextSeqNum:  pbOutput.GetNextSeqNum(),
		EndSeqNum:   pbOutput.GetEndSeqNum(),
	}
}
