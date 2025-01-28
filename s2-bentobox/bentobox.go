package s2bentobox

import (
	"context"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// Plugin name.
const PluginName = "s2"

func newBasinClient(config *Config) (*s2.BasinClient, error) {
	return s2.NewBasinClient(config.Basin, config.AuthToken)
}

func newStreamClient(config *Config, stream string) (*s2.StreamClient, error) {
	client, err := newBasinClient(config)
	if err != nil {
		return nil, err
	}

	// `s2.BasinClient.StreamClient` is a cheap operation.
	return client.StreamClient(stream), nil
}

func waitForClosers(ctx context.Context, closers ...<-chan struct{}) error {
	for _, closer := range closers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closer:
		}
	}

	return nil
}

type Logger interface {
	Tracef(template string, args ...any)
	Trace(message string)
	Debugf(template string, args ...any)
	Debug(message string)
	Infof(template string, args ...any)
	Info(message string)
	Warnf(template string, args ...any)
	Warn(message string)
	Errorf(template string, args ...any)
	Error(message string)
	With(keyValuePairs ...any) Logger
}

type Config struct {
	Basin     string
	AuthToken string
}
