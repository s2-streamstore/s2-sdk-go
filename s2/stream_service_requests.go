package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/pb"
)

type checkTailServiceRequest struct {
	Client pb.StreamServiceClient
	Stream string
	Tail   uint64
}

func (r *checkTailServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *checkTailServiceRequest) Send(ctx context.Context) error {
	req := &pb.CheckTailRequest{
		Stream: r.Stream,
	}

	pbResp, err := r.Client.CheckTail(ctx, req)
	if err != nil {
		return err
	}

	r.Tail = pbResp.GetNextSeqNum()
	return nil
}

type appendServiceRequest struct {
	Client pb.StreamServiceClient
	Stream string
	Input  *AppendInput
	Output *AppendOutput
}

func (r *appendServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelUnknown
}

func (r *appendServiceRequest) Send(ctx context.Context) error {
	pbInput := appendInputIntoProto(r.Stream, r.Input)
	req := &pb.AppendRequest{
		Input: pbInput,
	}

	pbResp, err := r.Client.Append(ctx, req)
	if err != nil {
		return err
	}

	r.Output = appendOutputFromProto(pbResp.GetOutput())
	return nil
}

type readServiceRequest struct {
	Client pb.StreamServiceClient
	Stream string
	Req    *ReadRequest
	Output ReadOutput
}

func (r *readServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *readServiceRequest) Send(ctx context.Context) error {
	var limit *pb.ReadLimit
	if r.Req.Limit != nil {
		limit = &pb.ReadLimit{
			Count: r.Req.Limit.Count,
			Bytes: r.Req.Limit.Bytes,
		}
	}
	req := &pb.ReadRequest{
		Stream:      r.Stream,
		StartSeqNum: r.Req.StartSeqNum,
		Limit:       limit,
	}

	pbResp, err := r.Client.Read(ctx, req)
	if err != nil {
		return err
	}

	output, err := readOutputFromProto(pbResp.GetOutput())
	if err != nil {
		return err
	}

	r.Output = output
	return nil
}
