package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type listStreamsServiceRequest struct {
	client pb.BasinServiceClient
	req    *ListStreamsRequest
}

func (r *listStreamsServiceRequest) idempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listStreamsServiceRequest) send(ctx context.Context) (any, error) {
	req := &pb.ListStreamsRequest{
		Prefix:     r.req.Prefix,
		StartAfter: r.req.StartAfter,
		Limit:      r.req.Limit,
	}

	pbResp, err := r.client.ListStreams(ctx, req)
	if err != nil {
		return nil, err
	}

	pbStreams := pbResp.GetStreams()
	streamInfos := make([]StreamInfo, 0, len(pbStreams))
	for _, pbInfo := range pbStreams {
		streamInfos = append(streamInfos, streamInfoFromProto(pbInfo))
	}

	return &ListStreamsResponse{
		Streams: streamInfos,
		HasMore: pbResp.GetHasMore(),
	}, nil
}
