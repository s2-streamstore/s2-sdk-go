package s2

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type idempotencyLevel uint

const (
	idempotencyLevelUnknown idempotencyLevel = iota
	idempotencyLevelIdempotent
	idempotencyLevelNoSideEffects
)

func (i idempotencyLevel) IsIdempotent() bool {
	return i == idempotencyLevelIdempotent || i == idempotencyLevelNoSideEffects
}

func (i idempotencyLevel) String() string {
	switch i {
	case idempotencyLevelUnknown:
		return "unknown"
	case idempotencyLevelIdempotent:
		return "idempotent"
	case idempotencyLevelNoSideEffects:
		return "no-side-effects"
	default:
		return "<unknown idempotency level>"
	}
}

type serviceRequest[T any] interface {
	IdempotencyLevel() idempotencyLevel
	Send(ctx context.Context) (T, error)
}

func shouldRetry[T any](r serviceRequest[T], err error) bool {
	if err == nil {
		return false
	}

	if !r.IdempotencyLevel().IsIdempotent() {
		return false
	}

	statusErr, ok := status.FromError(err)
	if !ok {
		return false
	}

	switch statusErr.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown:
		return true
	default:
		return false
	}
}

func ctxWithHeaders(ctx context.Context, pairs ...string) context.Context {
	headers := metadata.Pairs(pairs...)

	if existing, ok := metadata.FromOutgoingContext(ctx); ok {
		headers = metadata.Join(existing, headers)
	}

	return metadata.NewOutgoingContext(ctx, headers)
}
