package s2

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testRetryServiceRequest struct {
	idLevel  idempotencyLevel
	sendErr  error
	attempts uint
}

func (t *testRetryServiceRequest) IdempotencyLevel() idempotencyLevel {
	return t.idLevel
}

func (t *testRetryServiceRequest) IsStreaming() bool {
	return false
}

func (t *testRetryServiceRequest) Send(context.Context) (struct{}, error) {
	t.attempts++
	if t.attempts == 3 {
		return struct{}{}, nil
	}

	return struct{}{}, t.sendErr
}

func TestSendRetryable(t *testing.T) {
	type testCase struct {
		idLevel     idempotencyLevel
		sendErr     error
		shouldRetry bool
	}

	testCases := []testCase{
		{
			idLevel:     idempotencyLevelNoSideEffects,
			sendErr:     errors.New("hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelIdempotent,
			sendErr:     errors.New("hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelNoSideEffects,
			sendErr:     status.Error(codes.Unavailable, "hello"),
			shouldRetry: true,
		},
		{
			idLevel:     idempotencyLevelNoSideEffects,
			sendErr:     status.Error(codes.DeadlineExceeded, "hello"),
			shouldRetry: true,
		},
		{
			idLevel:     idempotencyLevelIdempotent,
			sendErr:     status.Error(codes.DeadlineExceeded, "hello"),
			shouldRetry: true,
		},
		{
			idLevel:     idempotencyLevelIdempotent,
			sendErr:     status.Error(codes.Unknown, "hello"),
			shouldRetry: true,
		},
		{
			idLevel:     idempotencyLevelUnknown,
			sendErr:     status.Error(codes.Unavailable, "hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelUnknown,
			sendErr:     status.Error(codes.Unknown, "hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelNoSideEffects,
			sendErr:     status.Error(codes.NotFound, "hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelIdempotent,
			sendErr:     status.Error(codes.InvalidArgument, "hello"),
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelUnknown,
			sendErr:     context.DeadlineExceeded,
			shouldRetry: false,
		},
		{
			idLevel:     idempotencyLevelIdempotent,
			sendErr:     context.DeadlineExceeded,
			shouldRetry: true,
		},
	}

	for _, tc := range testCases {
		for _, maxAttempts := range []uint{2, 4} {
			t.Run(fmt.Sprintf("%s %v", tc.idLevel, tc.sendErr), func(t *testing.T) {
				r := testRetryServiceRequest{
					idLevel: tc.idLevel,
					sendErr: tc.sendErr,
				}

				_, err := sendRetryableInner(context.TODO(), &r, 0, 0, maxAttempts)
				if tc.shouldRetry && maxAttempts >= 3 {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, tc.sendErr)
				}
			})
		}
	}
}
