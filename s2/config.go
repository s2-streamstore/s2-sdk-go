package s2

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	envAccessToken     = "S2_ACCESS_TOKEN"
	envAccountEndpoint = "S2_ACCOUNT_ENDPOINT"
	envBasinEndpoint   = "S2_BASIN_ENDPOINT"

	defaultScheme            = "https"
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

	if endpoint, ok := lookupEnvNonEmpty(envAccountEndpoint); ok {
		template, err := newEndpointTemplate(endpoint)
		if err != nil {
			panic(err)
		}
		cfg.AccountTemplate = template
	}

	if endpoint, ok := lookupEnvNonEmpty(envBasinEndpoint); ok {
		template, err := newEndpointTemplate(endpoint)
		if err != nil {
			panic(err)
		}
		cfg.BasinTemplate = template
	}

	return cfg
}

var schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

type endpointTemplate struct {
	raw                  string
	scheme               string
	hostTemplate         string
	port                 string
	pathTemplate         string
	explicitPathProvided bool
	hasBasinPlaceholder  bool
}

func lookupEnvNonEmpty(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(value) == "" {
		panic(fmt.Errorf("%s cannot be empty", name))
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
	delim := firstDelimiterIndex(trimmed, authorityStart)
	return delim != -1 && trimmed[delim] == '/'
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
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host == "localhost" || host == "127.0.0.1"
}

func normalizeForURLParsing(input string) string {
	trimmed := strings.TrimSpace(input)
	withScheme := trimmed
	if !schemePattern.MatchString(trimmed) {
		scheme := defaultScheme
		if isLocalhostEndpoint(trimmed) {
			scheme = "http"
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
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}

	hostTemplate := strings.ReplaceAll(parsed.Hostname(), basinPlaceholderSentinel, basinPlaceholder)
	port := parsed.Port()

	parsedPath := strings.ReplaceAll(parsed.EscapedPath(), basinPlaceholderSentinel, basinPlaceholder)
	parsedQuery := ""
	if parsed.RawQuery != "" {
		parsedQuery = "?" + strings.ReplaceAll(parsed.RawQuery, basinPlaceholderSentinel, basinPlaceholder)
	}
	parsedFragment := ""
	if parsed.Fragment != "" {
		parsedFragment = "#" + strings.ReplaceAll(parsed.Fragment, basinPlaceholderSentinel, basinPlaceholder)
	}

	pathTemplate := defaultAPIPath
	if explicitPathProvided {
		pathTemplate = parsedPath + parsedQuery + parsedFragment
	}

	return &endpointTemplate{
		raw:                  raw,
		scheme:               scheme,
		hostTemplate:         hostTemplate,
		port:                 port,
		pathTemplate:         pathTemplate,
		explicitPathProvided: explicitPathProvided,
		hasBasinPlaceholder:  strings.Contains(raw, basinPlaceholder),
	}, nil
}

func (t *endpointTemplate) baseURL(basin string) string {
	host := t.hostTemplate
	path := t.pathTemplate
	if basin != "" {
		host = strings.ReplaceAll(host, basinPlaceholder, basin)
		path = strings.ReplaceAll(path, basinPlaceholder, url.PathEscape(basin))
	}

	authority := host
	if t.port != "" {
		authority = host + ":" + t.port
	}
	return fmt.Sprintf("%s://%s%s", t.scheme, authority, path)
}
