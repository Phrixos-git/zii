package eval

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Summary struct {
	Executed          int            `json:"executed"`
	Pass              int            `json:"pass"`
	Warn              int            `json:"warn"`
	Fail              int            `json:"fail"`
	Skip              int            `json:"skip"`
	Warmup            int            `json:"warmup"`
	FinalSuccess      int            `json:"final_success"`
	CleanSuccessRate  *float64       `json:"clean_success_rate"`
	FinalSuccessRate  *float64       `json:"final_success_rate"`
	Issues            map[string]int `json:"issue_samples"`
	AllLatency        *Distribution  `json:"all_latency_ms"`
	SuccessfulLatency *Distribution  `json:"successful_latency_ms"`
}

func summarize(samples []Sample) Summary {
	s := Summary{Issues: map[string]int{}}
	var all, successful []float64
	for _, r := range samples {
		if r.Warmup {
			s.Warmup++
			continue
		}
		if r.Status == "SKIP" {
			s.Skip++
			continue
		}
		s.Executed++
		switch r.Status {
		case "PASS":
			s.Pass++
		case "WARN":
			s.Warn++
		case "FAIL":
			s.Fail++
		}
		if r.FinalContent && r.ErrorCode == "" {
			s.FinalSuccess++
		}
		all = append(all, r.Metrics.TotalMS)
		if r.Status == "PASS" || r.Status == "WARN" {
			successful = append(successful, r.Metrics.TotalMS)
		}
		for _, v := range unique(append(append([]string{}, r.Anomalies...), r.Failures...)) {
			s.Issues[v]++
		}
	}
	if s.Executed > 0 {
		s.CleanSuccessRate = ptr(float64(s.Pass) / float64(s.Executed))
		s.FinalSuccessRate = ptr(float64(s.FinalSuccess) / float64(s.Executed))
	}
	s.AllLatency = distribution(all)
	s.SuccessfulLatency = distribution(successful)
	return s
}

func CreateOutput(dir string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("output directory must be new: %w", err)
	}
	return os.OpenFile(filepath.Join(dir, "samples.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}
func WriteReport(dir string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var csvData bytes.Buffer
	w := csv.NewWriter(&csvData)
	if err = w.Write([]string{"case_id", "category", "repetition", "warmup", "status", "final_content", "error_code", "anomalies", "failures", "total_ms", "llm_http_ms", "tool_ms", "prompt_tokens", "completion_tokens", "reasoning_tokens", "requested_tool_calls", "tool_attempts", "retry_count", "completion_tokens_per_llm_second", "server_decode_tokens_per_second"}); err != nil {
		return err
	}
	for _, s := range r.Samples {
		m := s.Metrics
		if err = w.Write([]string{s.CaseID, s.Category, strconv.Itoa(s.Repetition), strconv.FormatBool(s.Warmup), s.Status, strconv.FormatBool(s.FinalContent), s.ErrorCode, strings.Join(s.Anomalies, ";"), strings.Join(s.Failures, ";"), number(m.TotalMS), number(m.LLMMS), number(m.ToolMS), intValue(m.PromptTokens), intValue(m.CompletionTokens), intValue(m.ReasoningTokens), strconv.Itoa(m.RequestedToolCalls), strconv.Itoa(m.ToolAttempts), strconv.Itoa(m.RetryCount), floatValue(m.CompletionPerLLMSecond), floatValue(m.ServerDecodeTPS)}); err != nil {
			return err
		}
	}
	w.Flush()
	if w.Error() != nil {
		return w.Error()
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"report.json", data}, {"results.csv", csvData.Bytes()}, {"summary.md", []byte(MarkdownReport(r))}} {
		if err := writeNew(filepath.Join(dir, f.name), f.data); err != nil {
			return err
		}
	}
	return nil
}
func writeNew(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func number(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
func intValue(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}
func floatValue(v *float64) string {
	if v == nil {
		return ""
	}
	return number(*v)
}
func md(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

func MarkdownReport(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Zii LLM Evaluation\n\n- Label: %s\n- Model: %s\n- Tool mode: %s\n- Complete: %t\n- Suite SHA-256: `%s`\n- Working tree revision: `%s` (dirty: %s)\n- Source SHA-256: `%s`\n- Token counter: %s\n\n", md(r.Options.Label), md(r.Profile.Model), r.Profile.ToolMode, r.Complete, r.SuiteHash, r.Source["worktree_revision"], r.Source["worktree_dirty"], r.Source["worktree_source_sha256"], r.Profile.TokenCounter)
	if r.SetupError != "" {
		fmt.Fprintf(&b, "Setup error: %s\n\n", r.SetupError)
	}
	s := r.Summary
	fmt.Fprintf(&b, "| Measured | PASS | WARN | FAIL | SKIP | Warmup (excluded) | Final answer returned |\n|---:|---:|---:|---:|---:|---:|---:|\n| %d | %d | %d | %d | %d | %d | %d |\n\n", s.Executed, s.Pass, s.Warn, s.Fail, s.Skip, s.Warmup, s.FinalSuccess)
	b.WriteString("PASS = assertions passed without anomalies. WARN = final answer passed, but an intermediate anomaly occurred. FAIL = final failure or assertion failure. These are mechanical checks, not general factual-quality grades.\n\n")
	b.WriteString("| Latency population | N | Mean ms | Median ms | P95 ms | Min ms | Max ms |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, v := range []struct {
		name string
		d    *Distribution
	}{{"All executed", s.AllLatency}, {"PASS + WARN", s.SuccessfulLatency}} {
		if v.d != nil {
			d := v.d
			fmt.Fprintf(&b, "| %s | %d | %.1f | %.1f | %.1f | %.1f | %.1f |\n", v.name, d.N, d.Mean, d.Median, d.P95, d.Min, d.Max)
		}
	}
	b.WriteString("\nP95 uses nearest rank; small samples do not support tail-latency conclusions. Warmup samples are excluded. HTTP time includes prefill, generation, and network time; completion_tokens_per_llm_second is not pure decode speed. server_decode_tokens_per_second is available only when every completion reports predicted_n and predicted_ms. Missing usage stays null (CSV blank). TTFT is not measured by this non-streaming runner.\n\n")
	if len(s.Issues) > 0 {
		b.WriteString("| Issue | Affected measured samples |\n|---|---:|\n")
		keys := []string{}
		for k := range s.Issues {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "| %s | %d |\n", md(k), s.Issues[k])
		}
		b.WriteString("\n")
	}
	b.WriteString("| Case | Run | Status | Total ms | Requested tools | Retries | Issues |\n|---|---:|---|---:|---:|---:|---|\n")
	for _, s := range r.Samples {
		if s.Warmup {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %.1f | %d | %d | %s |\n", s.CaseID, s.Repetition, s.Status, s.Metrics.TotalMS, s.Metrics.RequestedToolCalls, s.Metrics.RetryCount, md(strings.Join(unique(append(append([]string{}, s.Anomalies...), s.Failures...)), ", ")))
	}
	return b.String()
}

func ReadReport(path string) (Report, error) {
	var r Report
	b, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	if e != nil {
		return r, e
	}
	if r.Version != 1 {
		return r, fmt.Errorf("unsupported report version")
	}
	return r, nil
}
