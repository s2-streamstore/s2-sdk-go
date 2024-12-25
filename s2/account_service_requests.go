package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type listBasinsServiceRequest struct {
	Client pb.AccountServiceClient
	Req    *ListBasinsRequest
}

func (r *listBasinsServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listBasinsServiceRequest) Send(ctx context.Context) (any, error) {
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

type getBasinConfigRequest struct {
	Client pb.AccountServiceClient
	Basin  string
}

func (r *getBasinConfigRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *getBasinConfigRequest) Send(ctx context.Context) (any, error) {
	req := &pb.GetBasinConfigRequest{
		Basin: r.Basin,
	}

	pbResp, err := r.Client.GetBasinConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	return basinConfigFromProto(pbResp.GetConfig())
}
