package s2

import (
	"context"
	"fmt"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type idempotencyLevel uint

const (
	idempotencyLevelUnknown idempotencyLevel = iota
	idempotencyLevelIdempotent
	idempotencyLevelNoSideEffects
)

type serviceRequest interface {
	idempotencyLevel() idempotencyLevel
	send(ctx context.Context, resp any) error
}

func assertType[T any](r any) T {
	if v, ok := r.(T); ok {
		return v
	} else {
		var def T
		panic(fmt.Sprintf("expected %T but got %T", def, r))
	}
}

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

	pbResp, err := r.client.ListBasins(ctx, assertType[*pb.ListBasinsRequest](req))
	if err != nil {
		return nil, err
	}

	pbBasins := pbResp.GetBasins()
	basinInfos := make([]BasinInfo, 0, len(pbBasins))
	for _, info := range pbBasins {
		var state BasinState
		switch info.GetState() {
		case pb.BasinState_BASIN_STATE_UNSPECIFIED:
			state = BasinStateUnspecified
		case pb.BasinState_BASIN_STATE_ACTIVE:
			state = BasinStateActive
		case pb.BasinState_BASIN_STATE_CREATING:
			state = BasinStateCreating
		case pb.BasinState_BASIN_STATE_DELETING:
			state = BasinStateCreating
		default:
			return nil, fmt.Errorf("unknown basin state %d", info.GetState())
		}

		basinInfos = append(basinInfos, BasinInfo{
			Name:  info.GetName(),
			Scope: info.GetScope(),
			Cell:  info.GetCell(),
			State: state,
		})
	}

	return &ListBasinsResponse{
		Basins:  basinInfos,
		HasMore: pbResp.GetHasMore(),
	}, nil
}
