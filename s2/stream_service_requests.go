package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type checkTailServiceRequest struct {
	Client pb.StreamServiceClient
	Stream string
}

func (r *checkTailServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *checkTailServiceRequest) Send(ctx context.Context) (any, error) {
	req := &pb.CheckTailRequest{
		Stream: r.Stream,
	}

	pbResp, err := r.Client.CheckTail(ctx, req)
	if err != nil {
		return nil, err
	}

	return pbResp.GetNextSeqNum(), nil
}
