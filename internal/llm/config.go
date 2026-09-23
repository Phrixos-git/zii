package llm

import (
	"fmt"
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
	defaultBaseURL    = "http://127.0.0.1:8080"
)

// Config contains runtime settings for the OpenAI-compatible LLM client.
type Config struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	MaxTokens  int
	HTTPClient *http.Client
}

// Client sends requests to an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
	retryDelay time.Duration
	maxTokens  int
}

// NewClientFromEnv creates a client using the OR-15 runtime environment
// settings. It reads configuration only and does not contact the endpoint.
func NewClientFromEnv() (*Client, error) {
	baseURL := strings.TrimSpace(os.Getenv("LLM_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultBaseURL
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
	return NewClient(Config{
		BaseURL:   baseURL,
		Model:     os.Getenv("LLM_MODEL"),
		Timeout:   timeout,
		MaxTokens: maxTokens,
	})
}

// NewClient validates client configuration and prepares an HTTP client.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("llm: model must not be empty")
	}
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
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
		baseURL:    baseURL,
		model:      strings.TrimSpace(cfg.Model),
		httpClient: httpClient,
		retryDelay: defaultRetryDelay,
		maxTokens:  maxTokens,
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
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("llm: base URL must not contain user info, a query, or a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}
