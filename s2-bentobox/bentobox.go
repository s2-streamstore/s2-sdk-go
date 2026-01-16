package bentobox

import (
	"os"
	"strings"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

// Plugin name.
const PluginName = "s2"

// Environment variable names for endpoint configuration.
const (
	envAccountEndpoint = "S2_ACCOUNT_ENDPOINT"
	envBasinEndpoint   = "S2_BASIN_ENDPOINT"
)

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
	opts := &s2.ClientOptions{}

	// Check for endpoint overrides from environment (for s2-lite testing)
	if endpoint := os.Getenv(envAccountEndpoint); endpoint != "" {
		opts.BaseURL = parseEndpoint(endpoint) + "/v1"
	}

	if endpoint := os.Getenv(envBasinEndpoint); endpoint != "" {
		opts.MakeBasinBaseURL = makeBasinURLFunc(parseEndpoint(endpoint))
	}

	return s2.New(config.AccessToken, opts)
}

// parseEndpoint normalizes an endpoint string.
func parseEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}

	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		if isLocalhost(endpoint) {
			endpoint = "http://" + endpoint
		} else {
			endpoint = "https://" + endpoint
		}
	}

	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/v1")

	return endpoint
}

func isLocalhost(endpoint string) bool {
	host := endpoint
	if idx := strings.Index(endpoint, ":"); idx != -1 {
		host = endpoint[:idx]
	}
	return host == "localhost" || host == "127.0.0.1"
}

func makeBasinURLFunc(basinEndpoint string) func(basin string) string {
	if basinEndpoint == "" {
		return nil
	}

	if strings.Contains(basinEndpoint, "{basin}") {
		return func(basin string) string {
			return strings.ReplaceAll(basinEndpoint, "{basin}", basin) + "/v1"
		}
	}

	return func(basin string) string {
		return basinEndpoint + "/v1"
	}
}

func newStreamClient(config *Config, stream string) *s2.StreamClient {
	client := newClient(config)
	return client.Basin(config.Basin).Stream(s2.StreamName(stream))
}
