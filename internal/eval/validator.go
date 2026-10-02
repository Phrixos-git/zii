package eval

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/Phrixos-git/zii/internal/llm"
)

func validateSample(s *Sample, c Case, p Profile, answer string, err error) {
	anomalies := map[string]bool{}
	tools := map[string]int{}
	executed := map[string]int{}
	for _, e := range s.Trace.HTTP {
		if e.ErrorCode != "" {
			anomalies[e.ErrorCode] = true
		}
		for _, issue := range e.Issues {
			anomalies[issue] = true
		}
		for _, n := range e.ToolNames {
			tools[n]++
		}
	}
	for _, e := range s.Trace.Tools {
		executed[e.Name]++
		if e.ErrorCode != "" {
			if e.ErrorCode == "fixture_error" {
				s.Failures = append(s.Failures, "fixture_error")
			}
			if !(c.Expect.AllowToolErrors && e.ErrorCode == "mcp_error") {
				anomalies[e.ErrorCode] = true
			}
		}
	}
	if s.Metrics.RequestedToolCalls > p.MaxToolCalls {
		anomalies["tool_limit_exceeded"] = true
	}
	for name, limit := range map[string]int{"search_local": p.SearchLocalMaxCalls, "search_web": p.SearchWebMaxCalls, "fetch_page": p.FetchPageMaxCalls} {
		if tools[name] > limit {
			anomalies["per_tool_limit_exceeded:"+name] = true
		}
	}
	if err != nil {
		s.ErrorCode = runErrorCode(err)
		s.Failures = append(s.Failures, s.ErrorCode)
	}
	if !s.FinalContent {
		s.Failures = append(s.Failures, "missing_final_answer")
	}
	if c.Tools == "none" && s.Metrics.RequestedToolCalls > 0 {
		s.Failures = append(s.Failures, "unexpected_tool_call")
	}
	if s.Metrics.RequestedToolCalls < c.Expect.MinToolCalls {
		s.Failures = append(s.Failures, "too_few_tool_calls")
	}
	if c.Expect.MaxToolCalls != nil && s.Metrics.RequestedToolCalls > *c.Expect.MaxToolCalls {
		s.Failures = append(s.Failures, "too_many_tool_calls")
	}
	for _, n := range c.Expect.RequiredTools {
		if executed[n] == 0 {
			s.Failures = append(s.Failures, "required_tool_missing:"+n)
		}
	}
	for _, n := range c.Expect.ForbiddenTools {
		if tools[n] > 0 {
			s.Failures = append(s.Failures, "forbidden_tool:"+n)
		}
	}
	if c.Expect.Equals != "" && strings.TrimSpace(answer) != c.Expect.Equals {
		s.Failures = append(s.Failures, "answer_not_equal")
	}
	for _, v := range c.Expect.Contains {
		if !strings.Contains(answer, v) {
			s.Failures = append(s.Failures, "answer_missing_expected_text")
			break
		}
	}
	if c.Expect.Pattern != "" && !regexp.MustCompile(c.Expect.Pattern).MatchString(answer) {
		s.Failures = append(s.Failures, "answer_pattern_mismatch")
	}
	if c.Expect.MaxTotalMS > 0 && s.Metrics.TotalMS > c.Expect.MaxTotalMS {
		s.Failures = append(s.Failures, "latency_threshold_exceeded")
	}
	if markup(answer) {
		s.Failures = append(s.Failures, "final_tool_markup")
	}
	for k := range anomalies {
		s.Anomalies = append(s.Anomalies, k)
	}
	sort.Strings(s.Anomalies)
	s.Failures = unique(s.Failures)
	s.Status = "PASS"
	if len(s.Anomalies) > 0 {
		s.Status = "WARN"
	}
	if len(s.Failures) > 0 {
		s.Status = "FAIL"
	}
}
func unique(xs []string) []string {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	r := []string{}
	for x := range m {
		r = append(r, x)
	}
	sort.Strings(r)
	return r
}
func runErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, llm.ErrTruncated):
		return "truncated"
	case errors.Is(err, llm.ErrInvalidArguments):
		return "invalid_tool_arguments"
	case errors.Is(err, llm.ErrResponse):
		return "invalid_response"
	case strings.Contains(err.Error(), "final answer is empty"):
		return "empty_final"
	case strings.Contains(err.Error(), "tool-call markup"):
		return "tool_markup"
	default:
		return "orchestration_error"
	}
}
