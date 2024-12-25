package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type listBasinsServiceRequest struct {
	client pb.AccountServiceClient
	req    *ListBasinsRequest
}

func (r *listBasinsServiceRequest) idempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listBasinsServiceRequest) send(ctx context.Context) (any, error) {
	req := &pb.ListBasinsRequest{
		Prefix:     r.req.Prefix,
		StartAfter: r.req.StartAfter,
		Limit:      r.req.Limit,
	}

	pbResp, err := r.client.ListBasins(ctx, req)
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
