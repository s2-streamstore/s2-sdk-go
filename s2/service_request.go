package s2

import (
	"context"

	"google.golang.org/grpc/metadata"
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

func ctxWithHeaders(ctx context.Context, pairs ...string) context.Context {
	headers := metadata.Pairs(pairs...)

	if existing, ok := metadata.FromOutgoingContext(ctx); ok {
		headers = metadata.Join(existing, headers)
	}

	return metadata.NewOutgoingContext(ctx, headers)
}
