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

type nopLogger struct{}

func (nopLogger) Tracef(string, ...any) {}
func (nopLogger) Trace(string)          {}
func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Debug(string)          {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Info(string)           {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Warn(string)           {}
func (nopLogger) Errorf(string, ...any) {}
func (nopLogger) Error(string)          {}
func (nopLogger) With(...any) Logger    { return nopLogger{} }

func newClient(config *Config) *s2.Client {
	return s2.New(config.AccessToken, s2.LoadConfigFromEnv())
}

func newStreamClient(config *Config, stream string) *s2.StreamClient {
	client := newClient(config)
	return client.Basin(config.Basin).Stream(s2.StreamName(stream))
}
