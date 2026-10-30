package s2

import (
	"fmt"
	"strings"
	"testing"
)

func TestEndpointTemplate_DefaultsToV1(t *testing.T) {
	template, err := newEndpointTemplate("aws.s2.dev:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://aws.s2.dev:8443/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_DefaultsToHTTPForLocalhost(t *testing.T) {
	template, err := newEndpointTemplate("localhost:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "http://localhost:8443/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_ExplicitPath(t *testing.T) {
	template, err := newEndpointTemplate("https://aws.s2.dev/test/here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://aws.s2.dev/test/here"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_TrailingSlashExplicitPath(t *testing.T) {
	template, err := newEndpointTemplate("https://aws.s2.dev/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://aws.s2.dev/"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_BasinPlaceholderHost(t *testing.T) {
	template, err := newEndpointTemplate("https://{basin}.cell.aws.s2.dev:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("demo-basin")
	want := "https://demo-basin.cell.aws.s2.dev:8443/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_BasinPlaceholderPathEncoding(t *testing.T) {
	template, err := newEndpointTemplate("https://cell.aws.s2.dev/api/{basin}/v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("a/b")
	want := "https://cell.aws.s2.dev/api/a%2Fb/v2"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_IPv6WithPort(t *testing.T) {
	template, err := newEndpointTemplate("[::1]:8080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "http://[::1]:8080/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_IPv6WithoutPort(t *testing.T) {
	template, err := newEndpointTemplate("https://[::1]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://[::1]/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_RejectsQueryOrFragment(t *testing.T) {
	testCases := []struct {
		name     string
		endpoint string
	}{
		{name: "query string", endpoint: "https://aws.s2.dev?foo=bar"},
		{name: "query string without scheme", endpoint: "aws.s2.dev?foo=bar"},
		{name: "query string with explicit path", endpoint: "https://aws.s2.dev/basins?foo=bar"},
		{name: "fragment", endpoint: "https://aws.s2.dev#frag"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			template, err := newEndpointTemplate(tc.endpoint)
			if err == nil {
				t.Fatalf("expected error for endpoint %q, got template %#v", tc.endpoint, template)
			}
			if !strings.Contains(err.Error(), "query string or hash fragment") {
				t.Fatalf("expected error to mention query string or hash fragment, got %q", err.Error())
			}
		})
	}
}

func TestEndpointTemplate_AcceptsBareQuerySeparator(t *testing.T) {
	template, err := newEndpointTemplate("https://aws.s2.dev?")
	if err != nil {
		t.Fatalf("unexpected error for bare query separator: %v", err)
	}
	got := template.baseURL("")
	want := "https://aws.s2.dev/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_RejectsEmpty(t *testing.T) {
	if _, err := newEndpointTemplate("   "); err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

func TestLoadConfigFromEnv_RejectsWhitespaceEndpoints(t *testing.T) {
	testCases := []struct {
		name    string
		envName string
	}{
		{name: "account endpoint", envName: envAccountEndpoint},
		{name: "basin endpoint", envName: envBasinEndpoint},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envName, "   ")

			defer func() {
				if recover() == nil {
					t.Fatal("expected panic for empty endpoint")
				}
			}()

			loadConfigFromEnv()
		})
	}
}

func TestLoadConfigFromEnv_IgnoresEmptyEndpoints(t *testing.T) {
	t.Setenv(envAccountEndpoint, "")
	t.Setenv(envBasinEndpoint, "")

	cfg := loadConfigFromEnv()
	if cfg.AccountTemplate != nil {
		t.Fatal("expected empty account endpoint env var to be ignored")
	}
	if cfg.BasinTemplate != nil {
		t.Fatal("expected empty basin endpoint env var to be ignored")
	}
}

func TestLoadConfigFromEnv_RejectsQueryOrFragmentEndpoints(t *testing.T) {
	testCases := []struct {
		name     string
		envName  string
		endpoint string
	}{
		{name: "account endpoint with query", envName: envAccountEndpoint, endpoint: "https://aws.s2.dev?foo=bar"},
		{name: "basin endpoint with query", envName: envBasinEndpoint, endpoint: "https://aws.s2.dev?foo=bar"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envName, tc.endpoint)

			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic for endpoint with query string or fragment")
				}
				if !strings.Contains(fmt.Sprint(r), "query string or hash fragment") {
					t.Fatalf("expected panic to mention query string or hash fragment, got %v", r)
				}
			}()

			loadConfigFromEnv()
		})
	}
}

func TestNew_PanicsOnBaseURLWithQueryOrFragment(t *testing.T) {
	t.Run("BaseURL with query", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic for BaseURL with query string")
			}
		}()

		New("token", &ClientOptions{BaseURL: "https://aws.s2.dev?foo=bar"})
	})

	// MakeBasinBaseURL callback results are normalized lazily at Basin() time and must panic.
	t.Run("MakeBasinBaseURL with query", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic for MakeBasinBaseURL returning query endpoint")
			}
		}()

		client := New("token", &ClientOptions{
			MakeBasinBaseURL: func(string) string {
				return "https://aws.s2.dev?foo=bar"
			},
		})
		client.Basin("demo")
	})
}
