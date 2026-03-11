package s2

import "testing"

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

func TestEndpointTemplate_QueryParamsPreserved(t *testing.T) {
	template, err := newEndpointTemplate("https://aws.s2.dev?foo=bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://aws.s2.dev?foo=bar"
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
