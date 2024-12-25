package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type checkTailServiceRequest struct {
	client pb.StreamServiceClient
	stream string
}

func (r *checkTailServiceRequest) idempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *checkTailServiceRequest) send(ctx context.Context) (any, error) {
	req := &pb.CheckTailRequest{
		Stream: r.stream,
	}

	pbResp, err := r.client.CheckTail(ctx, req)
	if err != nil {
		return nil, err
	}

	return pbResp.GetNextSeqNum(), nil
}
