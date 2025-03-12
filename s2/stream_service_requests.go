package s2

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
)

type checkTailServiceRequest struct {
	Client pb.StreamServiceClient
	Stream string
}

func (r *checkTailServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *checkTailServiceRequest) IsStreaming() bool {
	return false
}

func (r *checkTailServiceRequest) Send(ctx context.Context) (uint64, error) {
	req := &pb.CheckTailRequest{
		Stream: r.Stream,
	}

	pbResp, err := r.Client.CheckTail(ctx, req)
	if err != nil {
		return 0, err
	}

	return pbResp.GetNextSeqNum(), nil
}

type appendServiceRequest struct {
	Client      pb.StreamServiceClient
	Compression bool
	Stream      string
	Input       *AppendInput
}

func (r *appendServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelUnknown
}

func (r *appendServiceRequest) IsStreaming() bool {
	return false
}

func (r *appendServiceRequest) Send(ctx context.Context) (*AppendOutput, error) {
	pbInput := appendInputIntoProto(r.Stream, r.Input)
	req := &pb.AppendRequest{
		Input: pbInput,
	}

	var callOpts []grpc.CallOption
	if r.Compression {
		callOpts = append(callOpts, grpc.UseCompressor(gzip.Name))
	}

	pbResp, err := r.Client.Append(ctx, req, callOpts...)
	if err != nil {
		return nil, err
	}

	return appendOutputFromProto(pbResp.GetOutput()), nil
}

type channel[S, R any] struct {
	Sender   Sender[S]
	Receiver Receiver[R]
}

type appendSessionServiceRequest struct {
	Client      pb.StreamServiceClient
	Stream      string
	Compression bool
}

func (r *appendSessionServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelUnknown
}

func (r *appendSessionServiceRequest) IsStreaming() bool {
	return true
}

func (r *appendSessionServiceRequest) Send(ctx context.Context) (*channel[*AppendInput, *AppendOutput], error) {
	var callOpts []grpc.CallOption
	if r.Compression {
		callOpts = append(callOpts, grpc.UseCompressor(gzip.Name))
	}

	pbResp, err := r.Client.AppendSession(ctx, callOpts...)
	if err != nil {
		return nil, err
	}

	sender := sendInner[*AppendInput, pb.AppendSessionRequest]{
		Client: pbResp,
		ConvertFn: func(ai *AppendInput) (*pb.AppendSessionRequest, error) {
			return &pb.AppendSessionRequest{
				Input: appendInputIntoProto(r.Stream, ai),
			}, nil
		},
	}

	receiver := recvInner[pb.AppendSessionResponse, *AppendOutput]{
		Client: pbResp,
		ConvertFn: func(asr *pb.AppendSessionResponse) (*AppendOutput, error) {
			return appendOutputFromProto(asr.GetOutput()), nil
		},
	}

	return &channel[*AppendInput, *AppendOutput]{
		Sender:   sender,
		Receiver: receiver,
	}, nil
}

type readServiceRequest struct {
	Client      pb.StreamServiceClient
	Stream      string
	Req         *ReadRequest
	Compression bool
}

func (r *readServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *readServiceRequest) IsStreaming() bool {
	return false
}

func (r *readServiceRequest) Send(ctx context.Context) (ReadOutput, error) {
	limit := &pb.ReadLimit{
		Count: r.Req.Limit.Count,
		Bytes: r.Req.Limit.Bytes,
	}

	req := &pb.ReadRequest{
		Stream:      r.Stream,
		StartSeqNum: r.Req.StartSeqNum,
		Limit:       limit,
	}

	var callOpts []grpc.CallOption
	if r.Compression {
		callOpts = append(callOpts, grpc.UseCompressor(gzip.Name))
	}

	pbResp, err := r.Client.Read(ctx, req, callOpts...)
	if err != nil {
		return nil, err
	}

	return readOutputFromProto(pbResp.GetOutput() /* heartbeats = */, false)
}

type readSessionServiceRequest struct {
	Client      pb.StreamServiceClient
	Stream      string
	Req         *ReadSessionRequest
	Compression bool
}

func (r *readSessionServiceRequest) IdempotencyLevel() idempotencyLevel {
	return idempotencyLevelNoSideEffects
}

func (r *readSessionServiceRequest) IsStreaming() bool {
	return true
}

func (r *readSessionServiceRequest) Send(ctx context.Context) (Receiver[ReadOutput], error) {
	limit := &pb.ReadLimit{
		Count: r.Req.Limit.Count,
		Bytes: r.Req.Limit.Bytes,
	}

	req := &pb.ReadSessionRequest{
		Stream:      r.Stream,
		StartSeqNum: r.Req.StartSeqNum,
		Limit:       limit,
		Heartbeats:  false,
	}

	var callOpts []grpc.CallOption
	if r.Compression {
		callOpts = append(callOpts, grpc.UseCompressor(gzip.Name))
	}

	pbResp, err := r.Client.ReadSession(ctx, req, callOpts...)
	if err != nil {
		return nil, err
	}

	var recv Receiver[ReadOutput] = recvInner[pb.ReadSessionResponse, ReadOutput]{
		Client: pbResp,
		ConvertFn: func(rsr *pb.ReadSessionResponse) (ReadOutput, error) {
			return readOutputFromProto(rsr.GetOutput() /* heartbeats = */, req.Heartbeats)
		},
	}

	if req.Heartbeats {
		recv = heartbeatReadRecv{inner: recv}
	}

	return recv, nil
}

type heartbeatReadRecv struct {
	inner Receiver[ReadOutput]
}

func (r heartbeatReadRecv) Recv() (ReadOutput, error) {
	for {
		oput, err := r.inner.Recv()
		if err != nil {
			return nil, err
		}

		if oput == nil {
			// TODO: Handle heartbeat messages.
			continue
		}

		return oput, nil
	}
}
