package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
)

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "test-model", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.retryDelay = 0
	return client
}

func TestChatUsesOpenAIRequestDefaultsAndParsesFinalAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		var got struct {
			Model             string          `json:"model"`
			Messages          []chat.Message  `json:"messages"`
			Stream            bool            `json:"stream"`
			MaxTokens         int             `json:"max_tokens"`
			ToolChoice        string          `json:"tool_choice"`
			ParallelToolCalls bool            `json:"parallel_tool_calls"`
			Tools             []requestTool   `json:"tools"`
			Temperature       json.RawMessage `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if got.Model != "test-model" || len(got.Messages) != 1 || got.Messages[0].Content != "hello" {
			t.Errorf("unexpected model/messages: %+v", got)
		}
		if got.Stream || got.MaxTokens != 4096 || got.ToolChoice != "auto" || got.ParallelToolCalls {
			t.Errorf("unexpected request defaults: %+v", got)
		}
		if len(got.Tools) != 1 || got.Tools[0].Type != "function" || got.Tools[0].Function.Name != "lookup" {
			t.Errorf("unexpected tools: %+v", got.Tools)
		}
		if len(got.Temperature) != 0 {
			t.Errorf("unexpected sampling override: %s", got.Temperature)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello back"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	completion, err := client.Chat(context.Background(), []chat.Message{{Role: "user", Content: "hello"}}, []ToolDefinition{{
		Name: "lookup", Description: "look up a value", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if completion.FinishReason != "stop" || completion.Message.Content != "hello back" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestChatNormalizesStringAndObjectToolArguments(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
	}{
		{name: "string", arguments: `"{\"query\":\"q\"}"`},
		{name: "object", arguments: `{"query":"q"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":%s}}]},"finish_reason":"tool_calls"}]}`, tt.arguments)
			}))
			defer server.Close()

			completion, err := newTestClient(t, server).Chat(context.Background(), []chat.Message{{Role: "user", Content: "find"}}, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if completion.FinishReason != "tool_calls" || len(completion.Message.ToolCalls) != 1 {
				t.Fatalf("completion = %+v", completion)
			}
			if got := string(completion.Message.ToolCalls[0].Function.Arguments); got != `{"query":"q"}` {
				t.Fatalf("normalized arguments = %s", got)
			}
		})
	}
}

func TestChatClassifiesFinishReasonsAndInvalidArguments(t *testing.T) {
	cases := []struct {
		name     string
		response string
		wantErr  error
	}{
		{name: "length", response: `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`, wantErr: ErrTruncated},
		{name: "invalid response JSON", response: `{"choices":[`, wantErr: ErrResponse},
		{name: "null finish reason", response: `{"choices":[{"message":{"role":"assistant"},"finish_reason":null}]}`, wantErr: ErrResponse},
		{name: "unknown finish reason", response: `{"choices":[{"message":{"role":"assistant"},"finish_reason":"unknown"}]}`, wantErr: ErrResponse},
		{name: "invalid arguments", response: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":"not json"}}]},"finish_reason":"tool_calls"}]}`, wantErr: ErrInvalidArguments},
		{name: "multiple choices", response: `{"choices":[{"message":{"role":"assistant"},"finish_reason":"stop"},{"message":{"role":"assistant"},"finish_reason":"stop"}]}`, wantErr: ErrResponse},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()
			_, err := newTestClient(t, server).Chat(context.Background(), []chat.Message{{Role: "user", Content: "q"}}, nil)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Chat error = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
		})
	}
}

func TestTransportTimesOutWithoutRuntimeDependency(t *testing.T) {
	transport := &timeoutRoundTripper{}
	client, err := NewClient(Config{BaseURL: "http://example.test", Model: "test-model", Timeout: time.Second, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	client.retryDelay = 0
	_, err = client.Chat(context.Background(), []chat.Message{{Role: "user", Content: "q"}}, nil)
	if err == nil {
		t.Fatal("Chat succeeded despite HTTP timeout")
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("HTTP attempts=%d, want one timeout retry", got)
	}
}

type timeoutNetworkError struct{}

func (timeoutNetworkError) Error() string   { return "request timed out" }
func (timeoutNetworkError) Timeout() bool   { return true }
func (timeoutNetworkError) Temporary() bool { return true }

type timeoutRoundTripper struct{ calls atomic.Int32 }

func (rt *timeoutRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return nil, timeoutNetworkError{}
}

func TestCountTokensUsesLlamaCPPInputTokensEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions/input_tokens" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions/input_tokens", r.Method, r.URL.Path)
		}
		var got struct {
			Model    string         `json:"model"`
			Messages []chat.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode token request: %v", err)
		}
		if got.Model != "test-model" || len(got.Messages) != 1 || got.Messages[0].Content != "history" {
			t.Errorf("token request = %+v", got)
		}
		_, _ = io.WriteString(w, `{"object":"response.input_tokens","input_tokens":17}`)
	}))
	defer server.Close()

	count, err := newTestClient(t, server).CountTokens(context.Background(), []chat.Message{{Role: "user", Content: "history"}})
	if err != nil || count != 17 {
		t.Fatalf("CountTokens = %d, %v; want 17, nil", count, err)
	}
}

func TestCountTokensRejectsMissingNegativeAndMalformedResults(t *testing.T) {
	for _, body := range []string{`{}`, `{"input_tokens":-1}`, `not json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := newTestClient(t, server).CountTokens(context.Background(), []chat.Message{{Role: "user", Content: "q"}})
		server.Close()
		if err == nil {
			t.Fatalf("CountTokens(%q) succeeded, want error", body)
		}
	}
}

func TestTransportRetriesOnlyServerErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "secret response body")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client := newTestClient(t, server)
	if _, err := client.Chat(context.Background(), []chat.Message{{Role: "user", Content: "q"}}, nil); err != nil {
		t.Fatalf("Chat after retry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}

	calls.Store(0)
	client.httpClient = server.Client()
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "secret response body")
	})
	_, err := client.Chat(context.Background(), []chat.Message{{Role: "user", Content: "q"}}, nil)
	if err == nil || strings.Contains(err.Error(), "secret response body") {
		t.Fatalf("4xx error = %v; want generic error without response body", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("4xx request count = %d, want 1", got)
	}
}

type temporaryNetworkError struct{}

func (temporaryNetworkError) Error() string   { return "temporary network failure" }
func (temporaryNetworkError) Timeout() bool   { return false }
func (temporaryNetworkError) Temporary() bool { return true }

type retryRoundTripper struct {
	calls atomic.Int32
}

func (rt *retryRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if rt.calls.Add(1) == 1 {
		return nil, temporaryNetworkError{}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)),
	}, nil
}

func TestTransportRetriesNetworkErrorOnce(t *testing.T) {
	roundTripper := &retryRoundTripper{}
	client, err := NewClient(Config{
		BaseURL:    "http://example.test",
		Model:      "test-model",
		HTTPClient: &http.Client{Transport: roundTripper},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.retryDelay = 0
	if _, err := client.Chat(context.Background(), []chat.Message{{Role: "user", Content: "q"}}, nil); err != nil {
		t.Fatalf("Chat after network retry: %v", err)
	}
	if got := roundTripper.calls.Load(); got != 2 {
		t.Fatalf("RoundTrip calls = %d, want 2", got)
	}
}

func TestWaitRetryRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetry(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitRetry error = %v, want context.Canceled", err)
	}
}

var _ net.Error = temporaryNetworkError{}
