package bentobox

import (
	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

// Plugin name.
const PluginName = "s2"

// Logger interface for bentobox logging.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
}

type Config struct {
	Basin     string
	AuthToken string
}

func newClient(config *Config) *s2.Client {
	return s2.New(config.AuthToken, nil)
}

func newStreamClient(config *Config, stream string) *s2.StreamClient {
	client := newClient(config)

	return client.Basin(config.Basin).Stream(s2.StreamName(stream))
}
