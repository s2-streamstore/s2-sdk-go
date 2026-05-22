package s2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLocationsClientList(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", req.Method)
		}
		if req.URL.String() != "http://account.test/v1/locations" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}

		return jsonResponse(http.StatusOK, "", `[{"name":"default","is_private":false},{"name":"private-a","is_private":true}]`), nil
	})

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test",
		HTTPClient: &http.Client{Transport: rt, Timeout: time.Second},
	})

	locations, err := client.Locations.List(context.Background())
	if err != nil {
		t.Fatalf("list locations: %v", err)
	}
	if len(locations) != 2 {
		t.Fatalf("expected 2 locations, got %#v", locations)
	}
	if locations[0].Name != "default" || locations[0].IsPrivate {
		t.Fatalf("unexpected first location: %#v", locations[0])
	}
	if locations[1].Name != "private-a" || !locations[1].IsPrivate {
		t.Fatalf("unexpected second location: %#v", locations[1])
	}
}

func TestLocationsClientGetDefault(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", req.Method)
		}
		if req.URL.String() != "http://account.test/v1/locations/default" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}

		return jsonResponse(http.StatusOK, "", `{"name":"default","is_private":false}`), nil
	})

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test",
		HTTPClient: &http.Client{Transport: rt, Timeout: time.Second},
	})

	location, err := client.Locations.GetDefault(context.Background())
	if err != nil {
		t.Fatalf("get default location: %v", err)
	}
	if location.Name != "default" || location.IsPrivate {
		t.Fatalf("unexpected default location: %#v", location)
	}
}

func TestLocationsClientSetDefault(t *testing.T) {
	want := LocationName("private-a")

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", req.Method)
		}
		if req.URL.String() != "http://account.test/v1/locations/default" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}
		if req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected content type: %q", req.Header.Get("Content-Type"))
		}

		var payload LocationName
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload != want {
			t.Fatalf("unexpected request body: %q", payload)
		}

		return jsonResponse(http.StatusOK, "", `{"name":"private-a","is_private":true}`), nil
	})

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test",
		HTTPClient: &http.Client{Transport: rt, Timeout: time.Second},
	})

	location, err := client.Locations.SetDefault(context.Background(), want)
	if err != nil {
		t.Fatalf("set default location: %v", err)
	}
	if location.Name != want || !location.IsPrivate {
		t.Fatalf("unexpected default location: %#v", location)
	}
}

func TestLocationsClientSetDefaultEmptyLocation(t *testing.T) {
	client := New("token", &ClientOptions{BaseURL: "http://account.test"})
	if _, err := client.Locations.SetDefault(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty location")
	}
}

func TestLocationNameMarshalsAsJSONString(t *testing.T) {
	payload, err := json.Marshal(LocationName("private-a"))
	if err != nil {
		t.Fatalf("marshal location name: %v", err)
	}

	body := strings.TrimSpace(string(payload))
	if body != `"private-a"` {
		t.Fatalf("expected JSON string body, got %s", body)
	}
}

func TestLocationInfoUnmarshal(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"name":"private-a","is_private":true}`))
	resp := &http.Response{Body: body}
	defer resp.Body.Close()

	var location LocationInfo
	if err := json.NewDecoder(resp.Body).Decode(&location); err != nil {
		t.Fatalf("decode location info: %v", err)
	}
	if location.Name != "private-a" || !location.IsPrivate {
		t.Fatalf("unexpected location info: %#v", location)
	}
}
