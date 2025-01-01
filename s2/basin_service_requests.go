package s2

import (
	"context"

	"github.com/google/uuid"
	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type listStreamsServiceRequest struct {
	Client pb.BasinServiceClient
	Req    *ListStreamsRequest
	Resp   *ListStreamsResponse
}

func (r *listStreamsServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *listStreamsServiceRequest) Send(ctx context.Context) error {
	req := &pb.ListStreamsRequest{
		Prefix:     r.Req.Prefix,
		StartAfter: r.Req.StartAfter,
		Limit:      r.Req.Limit,
	}

	pbResp, err := r.Client.ListStreams(ctx, req)
	if err != nil {
		return err
	}

	pbStreams := pbResp.GetStreams()
	streamInfos := make([]StreamInfo, 0, len(pbStreams))
	for _, pbInfo := range pbStreams {
		streamInfos = append(streamInfos, streamInfoFromProto(pbInfo))
	}

	r.Resp = &ListStreamsResponse{
		Streams: streamInfos,
		HasMore: pbResp.GetHasMore(),
	}
	return nil
}

type createStreamServiceRequest struct {
	Client pb.BasinServiceClient
	Req    *CreateStreamRequest
	ReqID  uuid.UUID
	Info   *StreamInfo
}

func (r *createStreamServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *createStreamServiceRequest) Send(ctx context.Context) error {
	config, err := streamConfigIntoProto(r.Req.Config)
	if err != nil {
		return err
	}

	req := &pb.CreateStreamRequest{
		Stream: r.Req.Stream,
		Config: config,
	}

	ctx = ctxWithHeader(ctx, "s2-request-token", r.ReqID.String())

	pbResp, err := r.Client.CreateStream(ctx, req)
	if err != nil {
		return err
	}

	info := streamInfoFromProto(pbResp.GetInfo())
	r.Info = &info
	return nil
}

type deleteStreamServiceRequest struct {
	Client pb.BasinServiceClient
	Req    *DeleteStreamRequest
}

func (r *deleteStreamServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *deleteStreamServiceRequest) Send(ctx context.Context) error {
	req := &pb.DeleteStreamRequest{
		Stream: r.Req.Stream,
	}

	_, err := r.Client.DeleteStream(ctx, req)
	if err != nil {
		statusErr, ok := status.FromError(err)
		if ok && statusErr.Code() == codes.NotFound && r.Req.IfExists {
			return nil
		}

		return err
	}

	return nil
}

type reconfigureStreamServiceRequest struct {
	Client        pb.BasinServiceClient
	Req           *ReconfigureStreamRequest
	UpdatedConfig *StreamConfig
}

func (r *reconfigureStreamServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelIdempotent
}

func (r *reconfigureStreamServiceRequest) Send(ctx context.Context) error {
	var streamConfig *pb.StreamConfig
	if r.Req.Config != nil {
		var err error
		streamConfig, err = streamConfigIntoProto(r.Req.Config)
		if err != nil {
			return err
		}
	}

	var mask *fieldmaskpb.FieldMask
	if r.Req.Mask != nil {
		mask = &fieldmaskpb.FieldMask{Paths: r.Req.Mask}
	}

	req := &pb.ReconfigureStreamRequest{
		Stream: r.Req.Stream,
		Config: streamConfig,
		Mask:   mask,
	}

	pbResp, err := r.Client.ReconfigureStream(ctx, req)
	if err != nil {
		return err
	}

	config, err := streamConfigFromProto(pbResp.GetConfig())
	if err != nil {
		return err
	}

	r.UpdatedConfig = config
	return nil
}

type getStreamConfigServiceRequest struct {
	Client pb.BasinServiceClient
	Stream string
	Config *StreamConfig
}

func (r *getStreamConfigServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *getStreamConfigServiceRequest) Send(ctx context.Context) error {
	req := &pb.GetStreamConfigRequest{
		Stream: r.Stream,
	}

	pbResp, err := r.Client.GetStreamConfig(ctx, req)
	if err != nil {
		return err
	}

	config, err := streamConfigFromProto(pbResp.GetConfig())
	if err != nil {
		return err
	}

	r.Config = config
	return nil
}
