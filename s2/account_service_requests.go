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
}

func (r *listBasinsServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listBasinsServiceRequest) Send(ctx context.Context) (*ListBasinsResponse, error) {
	req := &pb.ListBasinsRequest{
		Prefix:     r.Req.Prefix,
		StartAfter: r.Req.StartAfter,
		Limit:      r.Req.Limit,
	}

	pbResp, err := r.Client.ListBasins(ctx, req)
	if err != nil {
		return nil, err
	}

	pbBasins := pbResp.GetBasins()
	basinInfos := make([]BasinInfo, 0, len(pbBasins))

	for _, pbInfo := range pbBasins {
		info, err := basinInfoFromProto(pbInfo)
		if err != nil {
			return nil, err
		}

		basinInfos = append(basinInfos, info)
	}

	return &ListBasinsResponse{
		Basins:  basinInfos,
		HasMore: pbResp.GetHasMore(),
	}, nil
}

type createBasinServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *CreateBasinRequest
	ReqID  uuid.UUID
}

func (r *createBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *createBasinServiceRequest) Send(ctx context.Context) (*BasinInfo, error) {
	var basinConfig *pb.BasinConfig

	if r.Req.Config != nil {
		var err error

		basinConfig, err = basinConfigIntoProto(r.Req.Config)
		if err != nil {
			return nil, err
		}
	}

	req := &pb.CreateBasinRequest{
		Basin:  r.Req.Basin,
		Config: basinConfig,
	}

	ctx = ctxWithHeaders(ctx, "s2-request-token", r.ReqID.String())

	pbResp, err := r.Client.CreateBasin(ctx, req)
	if err != nil {
		return nil, err
	}

	info, err := basinInfoFromProto(pbResp.GetInfo())
	if err != nil {
		return nil, err
	}

	return &info, nil
}

type deleteBasinServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *DeleteBasinRequest
}

func (r *deleteBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *deleteBasinServiceRequest) Send(ctx context.Context) (struct{}, error) {
	req := &pb.DeleteBasinRequest{
		Basin: r.Req.Basin,
	}

	_, err := r.Client.DeleteBasin(ctx, req)
	if err != nil {
		statusErr, ok := status.FromError(err)
		if ok && statusErr.Code() == codes.NotFound && r.Req.IfExists {
			return struct{}{}, nil
		}

		return struct{}{}, err
	}

	return struct{}{}, nil
}

type reconfigureBasinServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *ReconfigureBasinRequest
}

func (r *reconfigureBasinServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *reconfigureBasinServiceRequest) Send(ctx context.Context) (*BasinConfig, error) {
	var basinConfig *pb.BasinConfig

	if r.Req.Config != nil {
		var err error

		basinConfig, err = basinConfigIntoProto(r.Req.Config)
		if err != nil {
			return nil, err
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
		return nil, err
	}

	return basinConfigFromProto(pbResp.GetConfig())
}

type getBasinConfigRequest struct {
	Client pb.AccountServiceClient
	Basin  string
}

func (r *getBasinConfigRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *getBasinConfigRequest) Send(ctx context.Context) (*BasinConfig, error) {
	req := &pb.GetBasinConfigRequest{
		Basin: r.Basin,
	}

	pbResp, err := r.Client.GetBasinConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	return basinConfigFromProto(pbResp.GetConfig())
}
