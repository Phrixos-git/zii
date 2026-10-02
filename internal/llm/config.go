package llm

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout    = 180 * time.Second
	defaultRetryDelay = 2 * time.Second
	defaultMaxTokens  = 4096
	defaultProfiles   = "config/model_profiles.yaml"
)

// Config contains runtime settings for the OpenAI-compatible LLM client.
type Config struct {
	Profile              ModelProfile
	ReasoningEffort      string
	ThinkingBudgetTokens int
	Timeout              time.Duration
	MaxTokens            int
	HTTPClient           *http.Client
}

// Client sends requests to an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	baseURL              string
	model                string
	profile              ModelProfile
	reasoningEffort      string
	thinkingBudgetTokens int
	httpClient           *http.Client
	retryDelay           time.Duration
	maxTokens            int
}

// NewClientFromEnv creates a client using the OR-15 runtime environment
// settings. It reads configuration only and does not contact the endpoint.
func NewClientFromEnv() (*Client, error) {
	profilePath := strings.TrimSpace(os.Getenv("LLM_PROFILE_CONFIG"))
	if profilePath == "" {
		profilePath = defaultProfiles
	}
	modelID := os.Getenv("LLM_MODEL")
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("llm: LLM_MODEL must name a profile in %s", profilePath)
	}
	if strings.TrimSpace(modelID) != modelID {
		return nil, fmt.Errorf("llm: LLM_MODEL must not have surrounding whitespace")
	}
	profile, err := LoadModelProfile(profilePath, modelID)
	if err != nil {
		return nil, err
	}
	if legacyURL := strings.TrimSpace(os.Getenv("LLM_BASE_URL")); legacyURL != "" {
		legacy, legacyErr := normalizeBaseURL(legacyURL)
		selected, selectedErr := normalizeBaseURL(profile.Endpoint)
		if legacyErr != nil || selectedErr != nil || legacy != selected {
			slog.Warn("LLM_BASE_URL is ignored; the selected model profile endpoint is authoritative", "component", "llm", "event", "legacy_llm_base_url_ignored", "model_profile", profile.ID)
		}
	}
	timeout := defaultTimeout
	if value := strings.TrimSpace(os.Getenv("LLM_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("llm: LLM_TIMEOUT must be a positive duration")
		}
		timeout = parsed
	}
	maxTokens := defaultMaxTokens
	if value := strings.TrimSpace(os.Getenv("LLM_MAX_TOKENS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("llm: LLM_MAX_TOKENS must be a positive integer")
		}
		maxTokens = parsed
	}
	thinkingBudget := 0
	if value := strings.TrimSpace(os.Getenv("LLM_THINKING_BUDGET_TOKENS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("llm: LLM_THINKING_BUDGET_TOKENS must be a non-negative integer")
		}
		thinkingBudget = parsed
	}
	return NewClient(Config{
		Profile:              profile,
		ReasoningEffort:      strings.TrimSpace(os.Getenv("LLM_REASONING_EFFORT")),
		ThinkingBudgetTokens: thinkingBudget,
		Timeout:              timeout,
		MaxTokens:            maxTokens,
	})
}

// NewClient validates client configuration and prepares an HTTP client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ThinkingBudgetTokens < 0 {
		return nil, fmt.Errorf("llm: thinking budget must not be negative, got %d", cfg.ThinkingBudgetTokens)
	}
	profile := cfg.Profile
	if err := validateModelProfile(profile); err != nil {
		return nil, fmt.Errorf("llm: invalid model profile: %w", err)
	}
	baseURL, err := normalizeBaseURL(profile.Endpoint)
	if err != nil {
		return nil, err
	}
	profile.Endpoint = baseURL
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("llm: timeout must not be negative, got %s", timeout)
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxTokens := cfg.MaxTokens
	if maxTokens < 0 {
		return nil, fmt.Errorf("llm: max tokens must not be negative, got %d", maxTokens)
	}
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	reasoningEffort := strings.TrimSpace(cfg.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = profile.Defaults.ReasoningEffort
	}
	if profile.Capabilities.ReasoningEffort && reasoningEffort != "" {
		found := false
		for _, supported := range profile.SupportedReasoningEfforts {
			if reasoningEffort == supported {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("llm: reasoning effort %q is not supported by model profile %q", reasoningEffort, profile.ID)
		}
	}
	thinkingBudget := cfg.ThinkingBudgetTokens
	if thinkingBudget == 0 && profile.Defaults.ThinkingBudgetTokens != nil {
		thinkingBudget = *profile.Defaults.ThinkingBudgetTokens
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	} else {
		clientCopy := *httpClient
		if clientCopy.Timeout == 0 || clientCopy.Timeout > timeout {
			clientCopy.Timeout = timeout
		}
		httpClient = &clientCopy
	}
	return &Client{
		baseURL:              baseURL,
		model:                profile.Model,
		profile:              profile,
		reasoningEffort:      reasoningEffort,
		thinkingBudgetTokens: thinkingBudget,
		httpClient:           httpClient,
		retryDelay:           defaultRetryDelay,
		maxTokens:            maxTokens,
	}, nil
}

func normalizeBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("llm: base URL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("llm: parse base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("llm: base URL must be an absolute http or https URL with a host")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("llm: base URL must not contain user info, a query, or a fragment")
	}
	if port := u.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", fmt.Errorf("llm: base URL port must be between 1 and 65535")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}
