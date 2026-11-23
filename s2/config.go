package s2

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	envAccessToken     = "S2_ACCESS_TOKEN"
	envAccountEndpoint = "S2_ACCOUNT_ENDPOINT"
	envBasinEndpoint   = "S2_BASIN_ENDPOINT"

	schemeHTTP               = "http"
	schemeHTTPS              = "https"
	defaultScheme            = schemeHTTPS
	defaultAPIPath           = "/v1"
	basinPlaceholder         = "{basin}"
	basinPlaceholderSentinel = "__basin__"
)

type Config struct {
	AccessToken     string
	AccountTemplate *endpointTemplate
	BasinTemplate   *endpointTemplate
}

// Returns ClientOptions with endpoints from S2_ACCOUNT_ENDPOINT and S2_BASIN_ENDPOINT.
func LoadConfigFromEnv() *ClientOptions {
	cfg := loadConfigFromEnv()
	cfg.requireEndpointPair()
	opts := &ClientOptions{}
	if cfg.AccountTemplate != nil {
		opts.BaseURL = cfg.AccountTemplate.baseURL("")
	}
	if cfg.BasinTemplate != nil {
		opts.MakeBasinBaseURL = cfg.BasinTemplate.baseURL
	}
	return opts
}

func loadConfigFromEnv() *Config {
	cfg := &Config{
		AccessToken: os.Getenv(envAccessToken),
	}

	if endpoint, ok := lookupOptionalEndpointEnv(envAccountEndpoint); ok {
		template, err := newEndpointTemplate(endpoint)
		if err != nil {
			panic(err)
		}
		cfg.AccountTemplate = template
	}

	if endpoint, ok := lookupOptionalEndpointEnv(envBasinEndpoint); ok {
		template, err := newEndpointTemplate(endpoint)
		if err != nil {
			panic(err)
		}
		cfg.BasinTemplate = template
	}

	return cfg
}

// Options override the environment, so NewFromEnvironment only checks this
// when they leave both endpoints unset; New checks the merged pair.
func (cfg *Config) requireEndpointPair() {
	if (cfg.AccountTemplate == nil) != (cfg.BasinTemplate == nil) {
		panic(newValidationError(envAccountEndpoint + " and " + envBasinEndpoint + " must be set together"))
	}
}

var schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

type endpointTemplate struct {
	raw                  string
	scheme               string
	hostTemplate         string
	port                 string
	pathTemplate         string
	explicitPathProvided bool
}

func lookupOptionalEndpointEnv(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

func hasExplicitPath(input string) bool {
	trimmed := strings.TrimSpace(input)
	authorityStart := 0
	if schemePattern.MatchString(trimmed) {
		if idx := strings.Index(trimmed, "://"); idx != -1 {
			authorityStart = idx + len("://")
		}
	}
	return firstDelimiterIndex(trimmed, authorityStart) != -1
}

func firstDelimiterIndex(value string, fromIndex int) int {
	slash := strings.Index(value[fromIndex:], "/")
	query := strings.Index(value[fromIndex:], "?")
	hash := strings.Index(value[fromIndex:], "#")
	min := -1
	for _, idx := range []int{slash, query, hash} {
		if idx == -1 {
			continue
		}
		abs := fromIndex + idx
		if min == -1 || abs < min {
			min = abs
		}
	}
	return min
}

func isLocalhostEndpoint(input string) bool {
	authority := input
	if idx := firstDelimiterIndex(input, 0); idx != -1 {
		authority = input[:idx]
	}
	host := authority
	if idx := strings.Index(host, "@"); idx != -1 {
		host = host[idx+1:]
	}
	// Handle IPv6 bracket notation: [::1] or [::1]:port
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end != -1 {
			host = host[1:end]
		}
	} else if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func normalizeForURLParsing(input string) string {
	trimmed := strings.TrimSpace(input)
	withScheme := trimmed
	if !schemePattern.MatchString(trimmed) {
		scheme := defaultScheme
		if isLocalhostEndpoint(trimmed) {
			scheme = schemeHTTP
		}
		withScheme = fmt.Sprintf("%s://%s", scheme, trimmed)
	}
	return strings.ReplaceAll(withScheme, basinPlaceholder, basinPlaceholderSentinel)
}

func newEndpointTemplate(endpoint string) (*endpointTemplate, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return nil, fmt.Errorf("endpoint cannot be empty")
	}

	explicitPathProvided := hasExplicitPath(raw)
	parsed, err := url.Parse(normalizeForURLParsing(raw))
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != schemeHTTP && scheme != schemeHTTPS {
		return nil, fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}
	// Request URLs are built by appending routes to the endpoint, so a query
	// or fragment would swallow the route.
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return nil, newValidationError("endpoint must not include a query or fragment")
	}
	if parsed.User != nil {
		return nil, newValidationError("endpoint must not include user info")
	}

	hostTemplate := strings.ReplaceAll(parsed.Hostname(), basinPlaceholderSentinel, basinPlaceholder)
	port := parsed.Port()
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, newValidationError(fmt.Sprintf("endpoint port must be between 1 and 65535, got %q", port))
		}
	}

	pathTemplate := defaultAPIPath
	if explicitPathProvided {
		pathTemplate = strings.ReplaceAll(parsed.EscapedPath(), basinPlaceholderSentinel, basinPlaceholder)
	}

	return &endpointTemplate{
		raw:                  raw,
		scheme:               scheme,
		hostTemplate:         hostTemplate,
		port:                 port,
		pathTemplate:         pathTemplate,
		explicitPathProvided: explicitPathProvided,
	}, nil
}

func (t *endpointTemplate) baseURL(basin string) string {
	host := t.hostTemplate
	path := t.pathTemplate
	if basin != "" {
		host = strings.ReplaceAll(host, basinPlaceholder, basin)
		path = strings.ReplaceAll(path, basinPlaceholder, url.PathEscape(basin))
	}

	var authority string
	if t.port != "" {
		authority = net.JoinHostPort(host, t.port)
	} else if strings.Contains(host, ":") {
		// IPv6 literal without port needs brackets per RFC 3986.
		authority = "[" + host + "]"
	} else {
		authority = host
	}
	return fmt.Sprintf("%s://%s%s", t.scheme, authority, path)
}

func normalizeBaseURL(endpoint string) string {
	template, err := newEndpointTemplate(endpoint)
	if err != nil {
		panic(err)
	}
	return template.baseURL("")
}
