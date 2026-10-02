package eval

import (
	"math"
	"sort"
)

// Pointer values distinguish unavailable measurements from a measured zero.
type HTTPEvent struct {
	Kind                  string   `json:"kind"`
	Call                  int      `json:"call"`
	MS                    float64  `json:"ms"`
	HTTPStatus            int      `json:"http_status,omitempty"`
	ErrorCode             string   `json:"error_code,omitempty"`
	FinishReason          string   `json:"finish_reason,omitempty"`
	ContentEmpty          *bool    `json:"content_empty,omitempty"`
	ReasoningPresent      bool     `json:"reasoning_content_present"`
	ReasoningMessagesSent int      `json:"reasoning_messages_sent"`
	ToolCalls             int      `json:"tool_calls"`
	ToolNames             []string `json:"tool_names,omitempty"`
	PromptTokens          *int     `json:"prompt_tokens"`
	CompletionTokens      *int     `json:"completion_tokens"`
	ReasoningTokens       *int     `json:"reasoning_tokens"`
	PredictedN            *int     `json:"server_predicted_n,omitempty"`
	PredictedMS           *float64 `json:"server_predicted_ms,omitempty"`
	RequestHash           string   `json:"request_sha256,omitempty"`
	Issues                []string `json:"issues,omitempty"`
}
type ToolEvent struct {
	Name      string  `json:"name"`
	MS        float64 `json:"ms"`
	ErrorCode string  `json:"error_code,omitempty"`
	Retry     bool    `json:"retry"`
}
type Trace struct {
	HTTP                []HTTPEvent `json:"http"`
	Tools               []ToolEvent `json:"tools"`
	LLMCalls            int         `json:"llm_calls"`
	TokenCountCalls     int         `json:"token_count_calls"`
	FinalizationRetries int         `json:"finalization_retries"`
	MCPReconnects       int         `json:"mcp_reconnects"`
}
type Metrics struct {
	TotalMS                float64  `json:"total_ms"`
	LLMMS                  float64  `json:"llm_http_ms"`
	TokenCountMS           float64  `json:"token_count_http_ms"`
	ToolMS                 float64  `json:"tool_ms"`
	LLMCalls               int      `json:"llm_calls"`
	HTTPAttempts           int      `json:"llm_http_attempts"`
	UsageCompleteAttempts  int      `json:"usage_complete_attempts"`
	PromptTokens           *int     `json:"prompt_tokens"`
	CompletionTokens       *int     `json:"completion_tokens"`
	ReasoningTokens        *int     `json:"reasoning_tokens"`
	CompletionPerLLMSecond *float64 `json:"completion_tokens_per_llm_second"`
	ServerDecodeTPS        *float64 `json:"server_decode_tokens_per_second"`
	RequestedToolCalls     int      `json:"requested_tool_calls"`
	ToolAttempts           int      `json:"tool_attempts"`
	ToolErrors             int      `json:"tool_errors"`
	LLMHTTPRetries         int      `json:"llm_http_retries"`
	TokenHTTPRetries       int      `json:"token_count_http_retries"`
	FinalizationRetries    int      `json:"finalization_retries"`
	ToolRetries            int      `json:"tool_client_retries"`
	RetryCount             int      `json:"retry_count"`
}

func ptr[T any](v T) *T { return &v }

func measure(t Trace, total float64) Metrics {
	m := Metrics{TotalMS: total, LLMCalls: t.LLMCalls, FinalizationRetries: t.FinalizationRetries, ToolAttempts: len(t.Tools)}
	var completions []HTTPEvent
	counts := map[string]map[int]int{"completion": {}, "token_count": {}}
	for _, e := range t.HTTP {
		counts[e.Kind][e.Call]++
		if e.Kind == "completion" {
			m.LLMMS += e.MS
			m.HTTPAttempts++
			m.RequestedToolCalls += e.ToolCalls
			completions = append(completions, e)
			if e.PromptTokens != nil && e.CompletionTokens != nil {
				m.UsageCompleteAttempts++
			}
		} else {
			m.TokenCountMS += e.MS
		}
	}
	for _, n := range counts["completion"] {
		m.LLMHTTPRetries += n - 1
	}
	for _, n := range counts["token_count"] {
		m.TokenHTTPRetries += n - 1
	}
	m.PromptTokens = sumTokens(completions, func(e HTTPEvent) *int { return e.PromptTokens })
	m.CompletionTokens = sumTokens(completions, func(e HTTPEvent) *int { return e.CompletionTokens })
	m.ReasoningTokens = sumTokens(completions, func(e HTTPEvent) *int { return e.ReasoningTokens })
	if m.CompletionTokens != nil && m.LLMMS > 0 {
		m.CompletionPerLLMSecond = ptr(float64(*m.CompletionTokens) * 1000 / m.LLMMS)
	}
	decodeN := 0
	decodeMS := 0.0
	decodeKnown := len(completions) > 0
	for _, e := range completions {
		if e.PredictedN == nil || e.PredictedMS == nil || *e.PredictedMS <= 0 {
			decodeKnown = false
			break
		}
		decodeN += *e.PredictedN
		decodeMS += *e.PredictedMS
	}
	if decodeKnown {
		m.ServerDecodeTPS = ptr(float64(decodeN) * 1000 / decodeMS)
	}
	for _, e := range t.Tools {
		m.ToolMS += e.MS
		if e.ErrorCode != "" {
			m.ToolErrors++
		}
		if e.Retry {
			m.ToolRetries++
		}
	}
	m.RetryCount = m.LLMHTTPRetries + m.TokenHTTPRetries + m.FinalizationRetries + m.ToolRetries
	return m
}
func sumTokens(events []HTTPEvent, get func(HTTPEvent) *int) *int {
	if len(events) == 0 {
		return nil
	}
	n := 0
	for _, e := range events {
		v := get(e)
		if v == nil {
			return nil
		}
		n += *v
	}
	return &n
}

type Distribution struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func distribution(values []float64) *Distribution {
	if len(values) == 0 {
		return nil
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	median := v[len(v)/2]
	if len(v)%2 == 0 {
		median = (median + v[len(v)/2-1]) / 2
	}
	return &Distribution{N: len(v), Mean: sum / float64(len(v)), Median: median, P95: v[int(math.Ceil(float64(len(v))*0.95))-1], Min: v[0], Max: v[len(v)-1]}
}
