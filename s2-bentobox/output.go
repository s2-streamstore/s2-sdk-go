package bentobox

import (
	"context"
	"errors"
	"sync"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

var ErrOutputClosed = errors.New("output closed")

type OutputConfig struct {
	*Config
	Stream       string
	FencingToken *string
	MaxInFlight  int
}

type Output struct {
	session      *s2.AppendSession
	fencingToken *string

	mu     sync.Mutex
	closed bool
}

func ConnectOutput(ctx context.Context, config *OutputConfig) (*Output, error) {
	stream := newStreamClient(config.Config, config.Stream)

	var opts *s2.AppendSessionOptions
	if config.MaxInFlight > 0 {
		opts = &s2.AppendSessionOptions{
			MaxInflightBatches: uint32(config.MaxInFlight),
		}
	}

	session, err := stream.AppendSession(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &Output{
		session:      session,
		fencingToken: config.FencingToken,
	}, nil
}

func (o *Output) WriteBatch(ctx context.Context, records []s2.AppendRecord) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return ErrOutputClosed
	}
	o.mu.Unlock()

	input := &s2.AppendInput{
		Records:      records,
		FencingToken: o.fencingToken,
	}

	future, err := o.session.Submit(input)
	if err != nil {
		return err
	}
	_, err = future.Wait(ctx)
	return err
}

func (o *Output) Close(ctx context.Context) error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()

	return o.session.Close()
}
