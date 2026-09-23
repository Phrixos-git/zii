package llm

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout    = 180 * time.Second
	defaultRetryDelay = 2 * time.Second
)

// Config contains runtime settings for the OpenAI-compatible LLM client.
type Config struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client sends requests to an OpenAI-compatible Chat Completions endpoint.
type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
	retryDelay time.Duration
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
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:    baseURL,
		model:      strings.TrimSpace(cfg.Model),
		httpClient: httpClient,
		retryDelay: defaultRetryDelay,
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
