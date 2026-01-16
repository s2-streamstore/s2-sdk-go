package bentobox

import (
	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

// Plugin name.
const PluginName = "s2"

type Config struct {
	Basin       string
	AccessToken string
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

func newClient(config *Config) *s2.Client {
	return s2.New(config.AccessToken, nil)
}

func newStreamClient(config *Config, stream string) *s2.StreamClient {
	client := newClient(config)
	return client.Basin(config.Basin).Stream(s2.StreamName(stream))
}
