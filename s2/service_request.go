package s2

import (
	"context"
	"fmt"

	"google.golang.org/grpc/metadata"
)

type idempotencyLevel uint

const (
	idempotencyLevelUnknown idempotencyLevel = iota
	idempotencyLevelIdempotent
	idempotencyLevelNoSideEffects
)

type serviceRequest interface {
	idempotencyLevel() idempotencyLevel
	send(ctx context.Context) (any, error)
}

func assertType[T any](r any) T {
	if v, ok := r.(T); ok {
		return v
	} else {
		var def T
		panic(fmt.Sprintf("expected %T but got %T", def, r))
	}
}

func ctxWithHeader(ctx context.Context, key, val string) context.Context {
	headers := metadata.Pairs(key, val)

	if existing, ok := metadata.FromOutgoingContext(ctx); ok {
		headers = metadata.Join(existing, headers)
	}

	return metadata.NewOutgoingContext(ctx, headers)
}
