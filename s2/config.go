package s2

import (
	"os"
	"strings"
)

const (
	envAccessToken     = "S2_ACCESS_TOKEN"
	envAccountEndpoint = "S2_ACCOUNT_ENDPOINT"
	envBasinEndpoint   = "S2_BASIN_ENDPOINT"
)

type Config struct {
	AccessToken     string
	AccountEndpoint string // Parsed with scheme, without /v1
	BasinEndpoint   string // Template (with {basin}) or fixed endpoint, without /v1
}

func loadConfigFromEnv() *Config {
	cfg := &Config{
		AccessToken: os.Getenv(envAccessToken),
	}

	if endpoint := os.Getenv(envAccountEndpoint); endpoint != "" {
		cfg.AccountEndpoint = parseEndpoint(endpoint)
	}

	if endpoint := os.Getenv(envBasinEndpoint); endpoint != "" {
		cfg.BasinEndpoint = parseEndpoint(endpoint)
	}

	return cfg
}

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
		// Template mode: substitute {basin} with actual basin name
		return func(basin string) string {
			return strings.ReplaceAll(basinEndpoint, "{basin}", basin) + "/v1"
		}
	}

	// Fixed endpoint mode: use the same endpoint for all basins
	return func(basin string) string {
		return basinEndpoint + "/v1"
	}
}
