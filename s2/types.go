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
		return nil, fmt.Errorf("unknown storage class %d", config.StorageClass)
	}

	switch r := config.RetentionPolicy.(type) {
	case RetentionPolicyAge:
		pbConfig.RetentionPolicy = &pb.StreamConfig_Age{Age: uint64(time.Duration(r) / time.Second)}
	case nil:
		pbConfig.RetentionPolicy = nil
	default:
		return nil, fmt.Errorf("unknown retention policy %T", r)
	}

	return pbConfig, nil
}

func basinConfigFromProto(pbConfig *pb.BasinConfig) (*BasinConfig, error) {
	var defaultStreamConfig *StreamConfig
	if pbConfig.DefaultStreamConfig != nil {
		var err error
		defaultStreamConfig, err = streamConfigFromProto(pbConfig.GetDefaultStreamConfig())
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
