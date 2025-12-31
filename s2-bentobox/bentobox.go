package bentobox

import (
	"log/slog"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

// Plugin name.
const PluginName = "s2"

type Config struct {
	Basin       string
	AccessToken string
	Logger      *slog.Logger
}

func newClient(config *Config) *s2.Client {
	var opts *s2.ClientOptions
	if config.Logger != nil {
		opts = &s2.ClientOptions{Logger: config.Logger}
	}

	return s2.New(config.AccessToken, opts)
}

func newStreamClient(config *Config, stream string) *s2.StreamClient {
	client := newClient(config)

	return client.Basin(config.Basin).Stream(s2.StreamName(stream))
}
