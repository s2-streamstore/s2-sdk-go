package s2

import (
	"context"

	"github.com/google/uuid"
	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type listBasinsServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *ListBasinsRequest
	Resp   *ListBasinsResponse
}

func (r *listBasinsServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listBasinsServiceRequest) Send(ctx context.Context) error {
	req := &pb.ListBasinsRequest{
		Prefix:     r.Req.Prefix,
		StartAfter: r.Req.StartAfter,
		Limit:      r.Req.Limit,
	}

	pbResp, err := r.Client.ListBasins(ctx, req)
	if err != nil {
		return err
	}

	pbBasins := pbResp.GetBasins()
	basinInfos := make([]BasinInfo, 0, len(pbBasins))
	for _, pbInfo := range pbBasins {
		info, err := basinInfoFromProto(pbInfo)
		if err != nil {
			return err
		}
		basinInfos = append(basinInfos, info)
	}

	r.Resp = &ListBasinsResponse{
		Basins:  basinInfos,
		HasMore: pbResp.GetHasMore(),
	}
	return nil
}

type createBasinServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *CreateBasinRequest
	ReqID  uuid.UUID
	Info   *BasinInfo
}

func (r *createBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *createBasinServiceRequest) Send(ctx context.Context) error {
	var basinConfig *pb.BasinConfig
	if r.Req.Config != nil {
		var err error
		basinConfig, err = basinConfigIntoProto(r.Req.Config)
		if err != nil {
			return err
		}
	}

	req := &pb.CreateBasinRequest{
		Basin:  r.Req.Basin,
		Config: basinConfig,
	}

	ctx = ctxWithHeader(ctx, "s2-request-token", r.ReqID.String())

	pbResp, err := r.Client.CreateBasin(ctx, req)
	if err != nil {
		return err
	}

	info, err := basinInfoFromProto(pbResp.GetInfo())
	if err != nil {
		return err
	}

	r.Info = &info
	return nil
}

type deleteBasinServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *DeleteBasinRequest
}

func (r *deleteBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *deleteBasinServiceRequest) Send(ctx context.Context) error {
	req := &pb.DeleteBasinRequest{
		Basin: r.Req.Basin,
	}

	_, err := r.Client.DeleteBasin(ctx, req)
	if err != nil {
		statusErr, ok := status.FromError(err)
		if ok && statusErr.Code() == codes.NotFound && r.Req.IfExists {
			return nil
		}

		return err
	}

	return nil
}

type reconfigureBasinServiceRequest struct {
	Client        pb.AccountServiceClient
	Req           *ReconfigureBasinRequest
	UpdatedConfig *BasinConfig
}

func (r *reconfigureBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *reconfigureBasinServiceRequest) Send(ctx context.Context) error {
	var basinConfig *pb.BasinConfig
	if r.Req.Config != nil {
		var err error
		basinConfig, err = basinConfigIntoProto(r.Req.Config)
		if err != nil {
			return err
		}
	}

	var mask *fieldmaskpb.FieldMask
	if r.Req.Mask != nil {
		mask = &fieldmaskpb.FieldMask{Paths: r.Req.Mask}
	}

	req := &pb.ReconfigureBasinRequest{
		Basin:  r.Req.Basin,
		Config: basinConfig,
		Mask:   mask,
	}

	pbResp, err := r.Client.ReconfigureBasin(ctx, req)
	if err != nil {
		return err
	}

	config, err := basinConfigFromProto(pbResp.GetConfig())
	if err != nil {
		return err
	}

	r.UpdatedConfig = config
	return nil
}

type getBasinConfigRequest struct {
	Client pb.AccountServiceClient
	Basin  string
	Config *BasinConfig
}

func (r *getBasinConfigRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *getBasinConfigRequest) Send(ctx context.Context) error {
	req := &pb.GetBasinConfigRequest{
		Basin: r.Basin,
	}

	pbResp, err := r.Client.GetBasinConfig(ctx, req)
	if err != nil {
		return err
	}

	config, err := basinConfigFromProto(pbResp.GetConfig())
	if err != nil {
		return err
	}

	r.Config = config
	return nil
}
