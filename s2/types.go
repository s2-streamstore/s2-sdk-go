package s2

import (
	"fmt"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type implRetentionPolicy interface {
	implRetentionPolicy()
}

func (a RetentionPolicyAge) implRetentionPolicy() {}

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
		return BasinInfo{}, fmt.Errorf("unknown basin state %d", pbInfo.GetState())
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
		return nil, fmt.Errorf("unknown storage class %d", pbConfig.GetStorageClass())
	}

	var retentionPolicy implRetentionPolicy
	switch r := pbConfig.GetRetentionPolicy().(type) {
	case *pb.StreamConfig_Age:
		retentionPolicy = RetentionPolicyAge(r.Age * uint64(time.Second))
	case nil:
		retentionPolicy = nil
	default:
		return nil, fmt.Errorf("unknown retention policy %T", r)
	}

	return &StreamConfig{
		StorageClass:    storageClass,
		RetentionPolicy: retentionPolicy,
	}, nil
}

func basinConfigFromProto(pbConfig *pb.BasinConfig) (*BasinConfig, error) {
	var (
		defaultStreamConfig *StreamConfig
		err                 error
	)
	if pbConfig.DefaultStreamConfig != nil {
		defaultStreamConfig, err = streamConfigFromProto(pbConfig.GetDefaultStreamConfig())
		if err != nil {
			return nil, err
		}
	}
	return &BasinConfig{
		DefaultStreamConfig: defaultStreamConfig,
	}, nil
}
