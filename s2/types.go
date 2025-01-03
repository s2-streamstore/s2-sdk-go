package s2

import (
	"errors"
	"fmt"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

// Errors.
var (
	ErrUnknownBasinState      = errors.New("unknown basin state")
	ErrUnknownStorageClass    = errors.New("unknown storage class")
	ErrUnknownRetentionPolicy = errors.New("unknown retention policy")
	ErrUnknownReadOutput      = errors.New("unknown read output")
)

type Receiver[T any] interface {
	Recv() (T, error)
}

type Sender[T any] interface {
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
	records := make([]*pb.AppendRecord, 0, len(input.Records))
	for i := range len(input.Records) {
		records = append(records, appendRecordIntoProto(&input.Records[i]))
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
