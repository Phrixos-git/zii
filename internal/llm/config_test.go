package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestNewClientDefaultsAndURLNormalization(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test/prefix///", Model: "test-model"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.baseURL != "https://example.test/prefix" {
		t.Fatalf("baseURL = %q, want normalized prefix", client.baseURL)
	}
	if client.model != "test-model" {
		t.Fatalf("model = %q, want test-model", client.model)
	}
	if client.httpClient.Timeout != 180*time.Second {
		t.Fatalf("HTTP timeout = %s, want 180s", client.httpClient.Timeout)
	}
	if client.retryDelay != 2*time.Second {
		t.Fatalf("retry delay = %s, want 2s", client.retryDelay)
	}
}

func TestNewClientUsesInjectedHTTPClient(t *testing.T) {
	httpClient := &http.Client{Timeout: 7 * time.Second}
	client, err := NewClient(Config{
		BaseURL:    "http://127.0.0.1:8080",
		Model:      "runtime-model",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.httpClient != httpClient {
		t.Fatal("NewClient did not preserve injected HTTP client")
	}
}

func TestNewClientRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "blank model", cfg: Config{BaseURL: "https://example.test", Model: " "}},
		{name: "blank URL", cfg: Config{Model: "model"}},
		{name: "relative URL", cfg: Config{BaseURL: "/local", Model: "model"}},
		{name: "unsupported scheme", cfg: Config{BaseURL: "ftp://example.test", Model: "model"}},
		{name: "missing host", cfg: Config{BaseURL: "http:///path", Model: "model"}},
		{name: "user info", cfg: Config{BaseURL: "http://user@example.test", Model: "model"}},
		{name: "query", cfg: Config{BaseURL: "http://example.test?secret=x", Model: "model"}},
		{name: "fragment", cfg: Config{BaseURL: "http://example.test#frag", Model: "model"}},
		{name: "negative timeout", cfg: Config{BaseURL: "http://example.test", Model: "model", Timeout: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.cfg); err == nil {
				t.Fatal("NewClient succeeded, want error")
			}
		})
	}
}
