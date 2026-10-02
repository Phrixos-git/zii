package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func completion(reason, content, reasoning string, calls []chat.ToolCall, usage any) string {
	v := map[string]any{"choices": []any{map[string]any{"finish_reason": reason, "message": map[string]any{"role": "assistant", "content": content, "reasoning_content": reasoning, "tool_calls": calls}}}}
	if usage != nil {
		v["usage"] = usage
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func tool(id, name, args string) chat.ToolCall {
	return chat.ToolCall{ID: id, Type: "function", Function: chat.FunctionCall{Name: name, Arguments: json.RawMessage(strconvJSON(args))}}
}
func strconvJSON(v string) string           { b, _ := json.Marshal(v); return string(b) }
func fixture(data map[string]any) []Fixture { return []Fixture{{Data: data}} }

func profileFor(url string) Profile {
	p := DefaultProfile()
	p.Name = "synthetic-test"
	p.Model = "scripted-test-model"
	p.BaseURL = url
	p.Timeout = "5s"
	return p
}
func runScript(t *testing.T, responses []string, statuses []int, c Case, modify func(*Profile)) Report {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions/input_tokens" {
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, `{"input_tokens":%d}`, len(b))
			return
		}
		i := int(calls.Add(1)) - 1
		if i >= len(responses) {
			t.Errorf("unexpected call %d", i+1)
			w.WriteHeader(400)
			return
		}
		if len(statuses) > i && statuses[i] != 0 {
			w.WriteHeader(statuses[i])
		}
		_, _ = io.WriteString(w, responses[i])
	}))
	defer server.Close()
	p := profileFor(server.URL)
	if modify != nil {
		modify(&p)
	}
	if c.ID == "" {
		c.ID = "test"
	}
	if c.Category == "" {
		c.Category = "unit"
	}
	if c.Prompt == "" {
		c.Prompt = "test prompt"
	}
	if c.Tools == "" {
		c.Tools = "auto"
	}
	r, err := Run(context.Background(), p, Suite{Version: 1, Cases: []Case{c}}, RunOptions{Runs: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Samples) != 1 {
		t.Fatalf("samples: %+v", r)
	}
	return r
}
func has(xs []string, want string) bool {
	for _, v := range xs {
		if v == want {
			return true
		}
	}
	return false
}

func TestAnomalyDetectionThroughProductionLoop(t *testing.T) {
	ok := completion("stop", "完了", "", nil, nil)
	cases := []struct {
		name          string
		responses     []string
		c             Case
		status, issue string
		retries       int
	}{
		{"normal", []string{ok}, Case{}, "PASS", "", 0},
		{"empty", []string{completion("stop", "  ", "private reasoning", nil, nil)}, Case{}, "FAIL", "empty_final", 0},
		{"finish", []string{completion("unexpected", "回答", "", nil, nil)}, Case{}, "FAIL", "invalid_finish_reason", 0},
		{"empty_tools", []string{completion("tool_calls", "", "", nil, nil)}, Case{}, "FAIL", "invalid_tool_call_count", 0},
		{"multiple_tools", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `{"query":"a"}`), tool("b", "search_web", `{"query":"b"}`)}, nil)}, Case{}, "FAIL", "invalid_tool_call_count", 0},
		{"bad_arguments", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `not-json`)}, nil)}, Case{}, "FAIL", "invalid_tool_arguments", 0},
		{"bad_schema", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `{"query":123}`)}, nil), ok}, Case{}, "WARN", "tool_schema_error", 0},
		{"unknown_tool", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "shell", `{"command":"echo hello"}`)}, nil), ok}, Case{}, "WARN", "unknown_tool", 0},
		{"disabled_tool", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `{"query":"a"}`)}, nil), ok}, Case{Tools: "none"}, "FAIL", "tool_call_when_disabled", 0},
		{"markup_recovery", []string{completion("stop", "<tool_call>bad</tool_call>", "", nil, nil), ok}, Case{}, "WARN", "tool_markup", 1},
		{"length_recovery", []string{completion("length", "unfinished", "", nil, nil), ok}, Case{}, "WARN", "length", 1},
		{"stop_with_tools", []string{completion("stop", "回答", "", []chat.ToolCall{tool("a", "search_web", `{"query":"a"}`)}, nil)}, Case{}, "FAIL", "stop_with_tool_calls", 0},
		{"bad_json", []string{`{"choices":`}, Case{}, "FAIL", "invalid_response_json", 0},
		{"fixture_missing", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `{"query":"a"}`)}, nil), ok}, Case{}, "FAIL", "fixture_error", 0},
		{"duplicate_tool", []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_local", `{"query":"a"}`)}, nil), completion("tool_calls", "", "", []chat.ToolCall{tool("b", "search_local", `{"query":"a"}`)}, nil), ok}, Case{Fixtures: map[string][]Fixture{"search_local": fixture(map[string]any{"results": []any{}})}}, "WARN", "duplicate_tool_call", 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := runScript(t, tt.responses, nil, tt.c, nil)
			s := r.Samples[0]
			if s.Status != tt.status {
				t.Fatalf("got %s want %s: %+v", s.Status, tt.status, s)
			}
			if tt.issue != "" && !has(s.Anomalies, tt.issue) {
				t.Fatalf("missing %s in %+v", tt.issue, s.Anomalies)
			}
			if s.Metrics.FinalizationRetries != tt.retries {
				t.Fatalf("retries: %+v", s.Metrics)
			}
		})
	}
}

func TestThreeTurnsMetricsAndPrivacy(t *testing.T) {
	var requestCount atomic.Int32
	t.Setenv("EVAL_TEST_KEY", "secret-key-do-not-save")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions/input_tokens" {
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, `{"input_tokens":%d}`, len(b))
			return
		}
		requestCount.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret-key-do-not-save" {
			t.Error("missing auth")
		}
		var input struct {
			Messages []chat.Message `json:"messages"`
			Seed     int            `json:"seed"`
		}
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			t.Error(e)
		}
		if input.Seed != 42 {
			t.Error("generation parameter missing")
		}
		n := 0
		for _, m := range input.Messages {
			if m.Role == "tool" {
				n++
				if m.ToolCallID == "" {
					t.Error("unpaired tool result")
				}
			}
		}
		usage := map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "completion_tokens_details": map[string]any{"reasoning_tokens": 5}}
		var body string
		switch n {
		case 0:
			body = completion("tool_calls", "", "private-reasoning-never-saved", []chat.ToolCall{tool("first", "search_local", `{"query":"test"}`)}, usage)
		case 1:
			if len(input.Messages) < 4 || len(input.Messages[2].ToolCalls) != 1 {
				t.Error("tool history missing")
			}
			if len(input.Messages) < 4 || input.Messages[2].ReasoningContent != "private-reasoning-never-saved" {
				t.Error("reasoning_content missing from second HTTP request")
			}
			body = completion("tool_calls", "", "private-reasoning-never-saved", []chat.ToolCall{tool("second", "search_web", `{"query":"test"}`)}, usage)
		case 2:
			if len(input.Messages) < 6 || input.Messages[2].ReasoningContent != "private-reasoning-never-saved" || input.Messages[4].ReasoningContent != "private-reasoning-never-saved" {
				t.Error("reasoning_content missing from third HTTP request")
			}
			body = completion("stop", "完了", "private-reasoning-never-saved", nil, usage)
		default:
			t.Errorf("unexpected tool count %d", n)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	p := profileFor(server.URL)
	p.APIKeyEnv = "EVAL_TEST_KEY"
	p.Parameters = map[string]any{"seed": 42}
	c := Case{ID: "multi", Category: "multi-tool", Tools: "auto", Prompt: "検索してください", Expect: Expect{MinToolCalls: 2, RequiredTools: []string{"search_local", "search_web"}, Equals: "完了"}, Fixtures: map[string][]Fixture{"search_local": fixture(map[string]any{"results": []any{}}), "search_web": fixture(map[string]any{"results": []any{}})}}
	r, err := Run(context.Background(), p, Suite{Version: 1, Cases: []Case{c}}, RunOptions{Runs: 2, Warmup: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 9 || r.Summary.Executed != 2 || r.Summary.Warmup != 1 || r.Summary.Pass != 2 {
		t.Fatalf("warmup/repeat: %+v", r.Summary)
	}
	for _, s := range r.Samples {
		m := s.Metrics
		if s.Status != "PASS" || m.ToolAttempts != 2 || m.RequestedToolCalls != 2 || m.LLMCalls != 3 || m.RetryCount != 0 {
			t.Fatalf("sample: %+v", s)
		}
		if m.PromptTokens == nil || *m.PromptTokens != 30 || *m.CompletionTokens != 60 || *m.ReasoningTokens != 15 {
			t.Fatalf("usage %+v", m)
		}
		if !s.Trace.HTTP[0].ReasoningPresent {
			t.Error("reasoning presence not recorded")
		}
	}
	encoded, _ := json.Marshal(r)
	for _, secret := range []string{"private-reasoning-never-saved", "secret-key-do-not-save"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("report leaked %s", secret)
		}
	}
	if r.Samples[0].FinalAnswer != "" {
		t.Fatal("answers must be opt-in")
	}
	dir := filepath.Join(t.TempDir(), "report")
	journal, e := CreateOutput(dir)
	if e != nil {
		t.Fatal(e)
	}
	_ = journal.Close()
	if e = WriteReport(dir, r); e != nil {
		t.Fatal(e)
	}
	loaded, e := ReadReport(filepath.Join(dir, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Compare(r, loaded); e != nil {
		t.Fatal(e)
	}
	if e = WriteReport(dir, r); e == nil {
		t.Fatal("must not overwrite existing reports")
	}
}

func TestAllowedParallelToolsAndNormalFinalizationMetrics(t *testing.T) {
	first := completion("tool_calls", "", "private", []chat.ToolCall{
		tool("local", "search_local", `{"query":"local"}`),
		tool("web", "search_web", `{"query":"web"}`),
	}, nil)
	c := Case{Expect: Expect{MinToolCalls: 2}, Fixtures: map[string][]Fixture{
		"search_local": fixture(map[string]any{"results": []any{}}),
		"search_web":   fixture(map[string]any{"results": []any{}}),
	}}
	r := runScript(t, []string{first, completion("stop", "done", "", nil, nil)}, nil, c, func(p *Profile) {
		cfg, err := p.clientConfig()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Profile.Capabilities.ParallelToolCalls = true
		p.ResolvedModelProfile = &cfg.Profile
		p.MaxToolCalls = 2
	})
	s := r.Samples[0]
	if s.Status != "PASS" || s.Metrics.ToolAttempts != 2 || s.Metrics.FinalizationRetries != 0 || s.Metrics.RetryCount != 0 {
		t.Fatalf("allowed tools or normal finalization mislabeled: %+v", s)
	}
	if r.Profile.ResolvedModelProfile == nil || !r.Profile.ResolvedModelProfile.Capabilities.ParallelToolCalls {
		t.Fatal("resolved capability snapshot missing from report")
	}
}

func TestCapabilityGatingReachesHTTPRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls", "reasoning_effort", "thinking_budget_tokens"} {
			if _, present := body[key]; present {
				t.Errorf("disabled capability emitted %s", key)
			}
		}
		// The production client accepts omitted role; evaluation must agree.
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"done"}}]}`)
	}))
	defer server.Close()
	p := profileFor(server.URL)
	p.ResolvedModelProfile = &llm.ModelProfile{ID: "plain", Model: p.Model, Endpoint: p.BaseURL}
	suite := Suite{Version: 1, Cases: []Case{{ID: "plain", Category: "basic", Prompt: "hello", Tools: "auto"}}}
	r, err := Run(context.Background(), p, suite, RunOptions{Runs: 1}, nil)
	if err != nil || r.Summary.Pass != 1 {
		t.Fatalf("capability gating: %v %+v", err, r)
	}
	p.Parameters = map[string]any{"reasoning_effort": "medium"}
	if p.Validate() == nil {
		t.Fatal("disabled capability was bypassed by evaluation parameters")
	}
}

func TestUsageAndRetryAccounting(t *testing.T) {
	ok := completion("stop", "answer", "", nil, map[string]any{"prompt_tokens": 2, "completion_tokens": 4})
	r := runScript(t, []string{`{"error":"secret error body"}`, ok}, []int{500, 200}, Case{}, nil)
	s := r.Samples[0]
	if s.Status != "WARN" || s.Metrics.LLMCalls != 1 || s.Metrics.HTTPAttempts != 2 || s.Metrics.LLMHTTPRetries != 1 {
		t.Fatalf("HTTP retry lost: %+v", s)
	}
	if s.Metrics.PromptTokens != nil || s.Metrics.CompletionTokens != nil {
		t.Fatal("unknown failed-attempt usage must not become zero")
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "secret error body") {
		t.Fatal("HTTP error body leaked")
	}
	trace := Trace{LLMCalls: 1, HTTP: []HTTPEvent{{Kind: "completion", Call: 1, MS: 100, PromptTokens: ptr(0), CompletionTokens: ptr(0), ReasoningTokens: ptr(0), PredictedN: ptr(4), PredictedMS: ptr(50.0)}}}
	m := measure(trace, 150)
	if m.CompletionTokens == nil || *m.CompletionTokens != 0 || m.ServerDecodeTPS == nil || *m.ServerDecodeTPS != 80 {
		t.Fatalf("known zeros or server timing broken: %+v", m)
	}
	m = measure(Trace{HTTP: []HTTPEvent{{Kind: "completion", Call: 1, MS: 100}}}, 100)
	if m.CompletionTokens != nil || m.CompletionPerLLMSecond != nil {
		t.Fatal("missing usage fabricated")
	}
}

func TestRetryOptionsKeepPrecedence(t *testing.T) {
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n.Add(1) == 1 {
			_, _ = io.WriteString(w, completion("length", "partial", "", nil, nil))
			return
		}
		if body["reasoning_effort"] != "medium" || body["max_tokens"] != float64(1536) || body["tool_choice"] != "none" {
			t.Errorf("retry altered: %+v", body)
		}
		_, _ = io.WriteString(w, completion("stop", "done", "", nil, nil))
	}))
	defer server.Close()
	p := profileFor(server.URL)
	p.Parameters = map[string]any{"reasoning_effort": "low"}
	r, e := Run(context.Background(), p, Suite{Version: 1, Cases: []Case{{ID: "retry", Category: "unit", Prompt: "test", Tools: "none"}}}, RunOptions{Runs: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.Summary.Warn != 1 {
		t.Fatal(r.Summary)
	}
}

func TestDeadlineAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	p := profileFor(server.URL)
	p.Timeout = "30ms"
	suite := Suite{Version: 1, Cases: []Case{{ID: "timeout", Category: "edge", Prompt: "hello", Tools: "none"}}}
	r, e := Run(context.Background(), p, suite, RunOptions{Runs: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.Samples[0].Status != "FAIL" || !has(r.Samples[0].Anomalies, "timeout") {
		t.Fatalf("timeout lost: %+v", r.Samples[0])
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, e = Run(ctx, p, suite, RunOptions{Runs: 3}, func(Sample) { cancel() })
	if e == nil || r.Complete || len(r.Samples) != 1 {
		t.Fatalf("partial run not preserved: complete %v, samples %d, err %v", r.Complete, len(r.Samples), e)
	}
	if _, e = Compare(r, r); e == nil {
		t.Fatal("must reject incomplete comparison")
	}
}

func TestExpectedToolErrorAndAssertions(t *testing.T) {
	c := Case{Expect: Expect{AllowToolErrors: true, MinToolCalls: 1, RequiredTools: []string{"search_web"}}, Fixtures: map[string][]Fixture{"search_web": {{IsError: true, Data: map[string]any{"code": "timeout"}}}}}
	r := runScript(t, []string{completion("tool_calls", "", "", []chat.ToolCall{tool("a", "search_web", `{"query":"a"}`)}, nil), completion("stop", "取得できませんでした", "", nil, nil)}, nil, c, nil)
	if r.Samples[0].Status != "PASS" || r.Samples[0].Metrics.ToolErrors != 1 {
		t.Fatalf("expected error: %+v", r.Samples[0])
	}
	r = runScript(t, []string{completion("stop", "wrong", "", nil, nil)}, nil, Case{Expect: Expect{Equals: "right"}}, nil)
	if r.Samples[0].Status != "FAIL" || !has(r.Samples[0].Failures, "answer_not_equal") {
		t.Fatal("assertion ignored")
	}
}

func TestComparePairingAndFailureLatency(t *testing.T) {
	suite := Suite{Version: 1, Cases: []Case{{ID: "a"}, {ID: "b"}}}
	a := Report{Version: 1, Complete: true, Suite: suite, SuiteHash: hash(suite), ProtocolHash: "same", Samples: []Sample{{CaseID: "a", Repetition: 1, Status: "PASS", Metrics: Metrics{TotalMS: 100}}, {CaseID: "b", Repetition: 1, Status: "PASS", Metrics: Metrics{TotalMS: 100}}}}
	b := a
	b.Samples = append([]Sample(nil), a.Samples...)
	b.Samples[0].Status = "FAIL"
	b.Samples[0].Metrics.TotalMS = 1
	b.Samples[1].Metrics.TotalMS = 120
	c, e := Compare(a, b)
	if e != nil {
		t.Fatal(e)
	}
	if c.Regressions != 1 || c.MatchedSuccessfulPairs != 1 || c.Pairs[0].DeltaMS != nil || c.MatchedLatencyChangePercent == nil || *c.MatchedLatencyChangePercent < 19.99 {
		t.Fatalf("misleading speed comparison: %+v", c)
	}
	b.SuiteHash = "different"
	if _, e = Compare(a, b); e == nil {
		t.Fatal("changed suite accepted")
	}
	b = a
	b.ProtocolHash = "different"
	if _, e = Compare(a, b); e == nil {
		t.Fatal("changed protocol accepted")
	}
	b = a
	b.Samples = append(append([]Sample{}, a.Samples...), a.Samples[0])
	if _, e = Compare(a, b); e == nil {
		t.Fatal("duplicate sample accepted")
	}
	d := distribution([]float64{4, 1, 2, 3})
	if d.N != 4 || d.Median != 2.5 || d.P95 != 4 || d.Mean != 2.5 {
		t.Fatal(d)
	}
}

func TestYAMLValidationAndShippedCases(t *testing.T) {
	s, e := LoadSuite("../../eval/testcases")
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Cases) != 23 {
		t.Fatalf("expected 23 shipped cases, got %d", len(s.Cases))
	}
	p, e := LoadProfile("../../eval/profiles/example.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if p.ToolMode != "fixture" {
		t.Fatal(p)
	}
	if p.ResolvedModelProfile == nil || p.ModelProfileID != "qwen" || !p.ResolvedModelProfile.Capabilities.ParallelToolCalls || p.ResolvedModelProfile.Defaults.ReasoningEffort != "medium" {
		t.Fatalf("production model profile was not resolved: %+v", p)
	}
	for name, body := range map[string]string{
		"unknown":         "version: 1\ncases:\n- id: test\n  category: x\n  prompt: x\n  promt: typo\n",
		"duplicate":       "version: 1\ncases:\n- {id: test, category: x, prompt: x}\n- {id: test, category: x, prompt: x}\n",
		"regex":           "version: 1\ncases:\n- id: test\n  category: x\n  prompt: x\n  expect: {pattern: '['}\n",
		"second_document": "version: 1\ncases: []\n---\nversion: 1\ncases: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.yaml")
			if e := os.WriteFile(path, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := LoadSuite(path); e == nil {
				t.Fatal("bad suite accepted")
			}
		})
	}
	p.BaseURL = "http://localhost:8080/v1"
	if p.Validate() == nil {
		t.Fatal("duplicate /v1 accepted")
	}
	if !reflect.DeepEqual(unique([]string{"b", "a", "b"}), []string{"a", "b"}) {
		t.Fatal("issue ordering unstable")
	}
}
