package llm

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func configTestProfile(id, model, endpoint string) ModelProfile {
	return ModelProfile{ID: id, Model: model, Endpoint: endpoint}
}

func TestNewClientDefaultsAndURLNormalization(t *testing.T) {
	client, err := NewClient(Config{Profile: configTestProfile("test-model", "test-model", "https://example.test/prefix///")})
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
		Profile:    configTestProfile("runtime-model", "runtime-model", "http://127.0.0.1:8080"),
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
	client, err = NewClient(Config{Profile: configTestProfile("model", "model", "https://example.test"), Timeout: 4 * time.Second, HTTPClient: noTimeout})
	if err != nil {
		t.Fatalf("NewClient with zero-timeout injection: %v", err)
	}
	if client.httpClient.Timeout != 4*time.Second || noTimeout.Timeout != 0 {
		t.Fatalf("effective timeout = %s, original timeout = %s", client.httpClient.Timeout, noTimeout.Timeout)
	}
}

func TestNewClientFromEnvUsesRuntimeDefaultsAndOverrides(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles.yaml")
	profilesYAML := `models:
  model-alias:
    model: server-model-a
    endpoint: http://profile-a.example.test:8080
    capabilities: {tools: true, reasoning: true, reasoning_content: true, reasoning_effort: true, thinking_budget: true, parallel_tool_calls: false}
    supported_reasoning_efforts: [medium]
    defaults: {reasoning_effort: medium, thinking_budget_tokens: 1024}
  other-alias:
    model: server-model-b
    endpoint: https://profile-b.example.test/api
    capabilities: {tools: false, reasoning: true, reasoning_content: true, reasoning_effort: true, thinking_budget: true, parallel_tool_calls: false}
    supported_reasoning_efforts: [medium]
    defaults: {reasoning_effort: medium, thinking_budget_tokens: null}
`
	if err := os.WriteFile(profilePath, []byte(profilesYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_PROFILE_CONFIG", profilePath)
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_MODEL", "model-alias")
	t.Setenv("LLM_TIMEOUT", "")
	t.Setenv("LLM_MAX_TOKENS", "")
	t.Setenv("LLM_REASONING_EFFORT", "")
	t.Setenv("LLM_THINKING_BUDGET_TOKENS", "")
	client, err := NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv defaults: %v", err)
	}
	if client.baseURL != "http://profile-a.example.test:8080" || client.model != "server-model-a" || client.profile.ID != "model-alias" || client.httpClient.Timeout != 180*time.Second || client.maxTokens != 4096 || client.reasoningEffort != "medium" || client.thinkingBudgetTokens != 1024 {
		t.Fatalf("default env configuration = %+v", client)
	}

	t.Setenv("LLM_BASE_URL", "https://legacy-override.example.test")
	t.Setenv("LLM_MODEL", "other-alias")
	t.Setenv("LLM_TIMEOUT", "45s")
	t.Setenv("LLM_MAX_TOKENS", "2048")
	t.Setenv("LLM_REASONING_EFFORT", "medium")
	t.Setenv("LLM_THINKING_BUDGET_TOKENS", "2048")
	client, err = NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv overrides: %v", err)
	}
	if client.baseURL != "https://profile-b.example.test/api" || client.model != "server-model-b" || client.httpClient.Timeout != 45*time.Second || client.maxTokens != 2048 || client.reasoningEffort != "medium" || client.thinkingBudgetTokens != 2048 {
		t.Fatalf("override env configuration = %+v", client)
	}
}

func TestNewClientFromEnvRejectsMissingModelAndInvalidLimits(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(profilePath, []byte(`models:
  model:
    model: model
    endpoint: http://127.0.0.1:8080
    capabilities: {tools: false, reasoning: false, reasoning_content: false, reasoning_effort: false, thinking_budget: false, parallel_tool_calls: false}
    supported_reasoning_efforts: []
    defaults: {reasoning_effort: "", thinking_budget_tokens: null}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_PROFILE_CONFIG", profilePath)
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_TIMEOUT", "")
	t.Setenv("LLM_MAX_TOKENS", "")
	if _, err := NewClientFromEnv(); err == nil {
		t.Fatal("NewClientFromEnv accepted a missing model")
	}

	t.Setenv("LLM_MODEL", "model")
	for name, value := range map[string]string{"LLM_TIMEOUT": "0s", "LLM_MAX_TOKENS": "-1", "LLM_THINKING_BUDGET_TOKENS": "invalid"} {
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
		{name: "blank profile ID", cfg: Config{Profile: configTestProfile("", "model", "https://example.test")}},
		{name: "blank model", cfg: Config{Profile: configTestProfile("id", " ", "https://example.test")}},
		{name: "blank URL", cfg: Config{Profile: configTestProfile("id", "model", "")}},
		{name: "relative URL", cfg: Config{Profile: configTestProfile("id", "model", "/local")}},
		{name: "unsupported scheme", cfg: Config{Profile: configTestProfile("id", "model", "ftp://example.test")}},
		{name: "missing host", cfg: Config{Profile: configTestProfile("id", "model", "http:///path")}},
		{name: "user info", cfg: Config{Profile: configTestProfile("id", "model", "http://user@example.test")}},
		{name: "query", cfg: Config{Profile: configTestProfile("id", "model", "http://example.test?secret=x")}},
		{name: "fragment", cfg: Config{Profile: configTestProfile("id", "model", "http://example.test#frag")}},
		{name: "negative timeout", cfg: Config{Profile: configTestProfile("id", "model", "http://example.test"), Timeout: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.cfg); err == nil {
				t.Fatal("NewClient succeeded, want error")
			}
		})
	}
}

func TestNewClientRejectsUnsupportedProfileEffortOverride(t *testing.T) {
	profile := ModelProfile{
		ID: "limited-efforts", Model: "model", Endpoint: "http://example.test",
		Capabilities:              Capabilities{Reasoning: true, ReasoningEffort: true},
		SupportedReasoningEfforts: []string{"medium"},
		Defaults:                  ModelDefaults{ReasoningEffort: "medium"},
	}
	if _, err := NewClient(Config{Profile: profile, ReasoningEffort: "high"}); err == nil {
		t.Fatal("NewClient accepted an unsupported reasoning effort override")
	}
}
