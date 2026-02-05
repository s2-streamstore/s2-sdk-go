package s2

import "testing"

func TestEndpointTemplate_DefaultsToV1(t *testing.T) {
	template, err := newEndpointTemplate("example.com:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://example.com:8443/v1"
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
	template, err := newEndpointTemplate("https://example.com/test/here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://example.com/test/here"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_TrailingSlashExplicitPath(t *testing.T) {
	template, err := newEndpointTemplate("https://example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("")
	want := "https://example.com/"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_BasinPlaceholderHost(t *testing.T) {
	template, err := newEndpointTemplate("https://{basin}.cell.example.com:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("demo-basin")
	want := "https://demo-basin.cell.example.com:8443/v1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_BasinPlaceholderPathEncoding(t *testing.T) {
	template, err := newEndpointTemplate("https://cell.example.com/api/{basin}/v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := template.baseURL("a/b")
	want := "https://cell.example.com/api/a%2Fb/v2"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestEndpointTemplate_RejectsEmpty(t *testing.T) {
	if _, err := newEndpointTemplate("   "); err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}
