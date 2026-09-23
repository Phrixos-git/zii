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
	if client.maxTokens != 4096 {
		t.Fatalf("max tokens = %d, want 4096", client.maxTokens)
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
	if client.httpClient == httpClient || client.httpClient.Timeout != httpClient.Timeout {
		t.Fatal("NewClient did not safely copy the injected HTTP client settings")
	}
	if client.httpClient.Transport != httpClient.Transport {
		t.Fatal("NewClient did not preserve injected HTTP transport")
	}
	if httpClient.Timeout != 7*time.Second {
		t.Fatalf("NewClient mutated injected client timeout: %s", httpClient.Timeout)
	}

	noTimeout := &http.Client{}
	client, err = NewClient(Config{BaseURL: "https://example.test", Model: "model", Timeout: 4 * time.Second, HTTPClient: noTimeout})
	if err != nil {
		t.Fatalf("NewClient with zero-timeout injection: %v", err)
	}
	if client.httpClient.Timeout != 4*time.Second || noTimeout.Timeout != 0 {
		t.Fatalf("effective timeout = %s, original timeout = %s", client.httpClient.Timeout, noTimeout.Timeout)
	}
}

func TestNewClientFromEnvUsesRuntimeDefaultsAndOverrides(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_MODEL", "model-alias")
	t.Setenv("LLM_TIMEOUT", "")
	t.Setenv("LLM_MAX_TOKENS", "")
	client, err := NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv defaults: %v", err)
	}
	if client.baseURL != defaultBaseURL || client.model != "model-alias" || client.httpClient.Timeout != 180*time.Second || client.maxTokens != 4096 {
		t.Fatalf("default env configuration = %+v", client)
	}

	t.Setenv("LLM_BASE_URL", "https://llm.example.test/api/")
	t.Setenv("LLM_MODEL", "other-alias")
	t.Setenv("LLM_TIMEOUT", "45s")
	t.Setenv("LLM_MAX_TOKENS", "2048")
	client, err = NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv overrides: %v", err)
	}
	if client.baseURL != "https://llm.example.test/api" || client.model != "other-alias" || client.httpClient.Timeout != 45*time.Second || client.maxTokens != 2048 {
		t.Fatalf("override env configuration = %+v", client)
	}
}

func TestNewClientFromEnvRejectsMissingModelAndInvalidLimits(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_TIMEOUT", "")
	t.Setenv("LLM_MAX_TOKENS", "")
	if _, err := NewClientFromEnv(); err == nil {
		t.Fatal("NewClientFromEnv accepted a missing model")
	}

	t.Setenv("LLM_MODEL", "model")
	for name, value := range map[string]string{"LLM_TIMEOUT": "0s", "LLM_MAX_TOKENS": "-1"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := NewClientFromEnv(); err == nil {
				t.Fatalf("NewClientFromEnv accepted %s=%q", name, value)
			}
		})
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
