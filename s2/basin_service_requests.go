package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type listStreamsServiceRequest struct {
	Client pb.BasinServiceClient
	Req    *ListStreamsRequest
}

func (r *listStreamsServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listStreamsServiceRequest) Send(ctx context.Context) (any, error) {
	req := &pb.ListStreamsRequest{
		Prefix:     r.Req.Prefix,
		StartAfter: r.Req.StartAfter,
		Limit:      r.Req.Limit,
	}

	pbResp, err := r.Client.ListStreams(ctx, req)
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

type getStreamConfigServiceRequest struct {
	Client pb.BasinServiceClient
	Stream string
}

func (r *getStreamConfigServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *getStreamConfigServiceRequest) Send(ctx context.Context) (any, error) {
	req := &pb.GetStreamConfigRequest{
		Stream: r.Stream,
	}

	pbResp, err := r.Client.GetStreamConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	return streamConfigFromProto(pbResp.GetConfig())
}
