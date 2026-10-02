package eval

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Pair struct {
	CaseID       string   `json:"case_id"`
	Repetition   int      `json:"repetition"`
	Before       string   `json:"before"`
	After        string   `json:"after"`
	Change       string   `json:"change"`
	BeforeMS     float64  `json:"before_ms"`
	AfterMS      float64  `json:"after_ms"`
	DeltaMS      *float64 `json:"delta_ms"`
	DeltaPercent *float64 `json:"delta_percent"`
}
type Comparison struct {
	Version                     int      `json:"version"`
	BeforeLabel                 string   `json:"before_label"`
	AfterLabel                  string   `json:"after_label"`
	Before                      Summary  `json:"before"`
	After                       Summary  `json:"after"`
	Regressions                 int      `json:"regressions"`
	Improvements                int      `json:"improvements"`
	ChangedProfileFields        []string `json:"changed_profile_fields"`
	SystemPromptChanged         bool     `json:"system_prompt_changed"`
	MatchedSuccessfulPairs      int      `json:"matched_successful_pairs"`
	MatchedLatencyChangePercent *float64 `json:"matched_latency_change_percent"`
	Pairs                       []Pair   `json:"pairs"`
}

func Compare(before, after Report) (Comparison, error) {
	c := Comparison{Version: 1, BeforeLabel: before.Options.Label, AfterLabel: after.Options.Label, Before: before.Summary, After: after.Summary, SystemPromptChanged: before.SystemPromptHash != after.SystemPromptHash}
	if !before.Complete || !after.Complete || before.SetupError != "" || after.SetupError != "" {
		return c, fmt.Errorf("cannot compare incomplete runs")
	}
	if before.SuiteHash == "" || before.SuiteHash != after.SuiteHash || before.SuiteHash != hash(before.Suite) || after.SuiteHash != hash(after.Suite) {
		return c, fmt.Errorf("test suites differ (including prompts, expectations and fixtures)")
	}
	if before.ProtocolHash == "" || before.ProtocolHash != after.ProtocolHash {
		return c, fmt.Errorf("protocol differs: align modes, limits, timeouts, token counter, schemas, repetitions and filters")
	}
	left, e := sampleIndex(before.Samples)
	if e != nil {
		return c, e
	}
	right, e := sampleIndex(after.Samples)
	if e != nil {
		return c, e
	}
	if len(left) != len(right) || len(left) == 0 {
		return c, fmt.Errorf("measured sample sets differ or are empty")
	}
	var bp, ap map[string]any
	b, _ := json.Marshal(before.Profile)
	a, _ := json.Marshal(after.Profile)
	_ = json.Unmarshal(b, &bp)
	_ = json.Unmarshal(a, &ap)
	keys := map[string]bool{}
	for k := range bp {
		keys[k] = true
	}
	for k := range ap {
		keys[k] = true
	}
	for k := range keys {
		if hash(bp[k]) != hash(ap[k]) {
			c.ChangedProfileFields = append(c.ChangedProfileFields, k)
		}
	}
	sort.Strings(c.ChangedProfileFields)
	order := []string{}
	for k := range left {
		order = append(order, k)
	}
	sort.Strings(order)
	rank := map[string]int{"PASS": 0, "WARN": 1, "FAIL": 2, "SKIP": 3}
	sumBefore, sumAfter := 0.0, 0.0
	for _, k := range order {
		x := left[k]
		y, ok := right[k]
		if !ok {
			return c, fmt.Errorf("unpaired sample %s", k)
		}
		if (x.Status == "SKIP") != (y.Status == "SKIP") {
			return c, fmt.Errorf("skip populations differ: %s", k)
		}
		p := Pair{CaseID: x.CaseID, Repetition: x.Repetition, Before: x.Status, After: y.Status, Change: "unchanged", BeforeMS: x.Metrics.TotalMS, AfterMS: y.Metrics.TotalMS}
		if rank[y.Status] > rank[x.Status] {
			p.Change = "regression"
			c.Regressions++
		} else if rank[y.Status] < rank[x.Status] {
			p.Change = "improvement"
			c.Improvements++
		}
		if rank[x.Status] <= 1 && rank[y.Status] <= 1 {
			c.MatchedSuccessfulPairs++
			p.DeltaMS = ptr(y.Metrics.TotalMS - x.Metrics.TotalMS)
			if x.Metrics.TotalMS > 0 {
				p.DeltaPercent = ptr((y.Metrics.TotalMS/x.Metrics.TotalMS - 1) * 100)
			}
			sumBefore += x.Metrics.TotalMS
			sumAfter += y.Metrics.TotalMS
		}
		c.Pairs = append(c.Pairs, p)
	}
	if sumBefore > 0 {
		c.MatchedLatencyChangePercent = ptr((sumAfter/sumBefore - 1) * 100)
	}
	return c, nil
}
func sampleIndex(samples []Sample) (map[string]Sample, error) {
	m := map[string]Sample{}
	for _, s := range samples {
		if s.Warmup {
			continue
		}
		if s.Status != "PASS" && s.Status != "WARN" && s.Status != "FAIL" && s.Status != "SKIP" {
			return nil, fmt.Errorf("invalid sample status")
		}
		k := s.CaseID + "/" + strconv.Itoa(s.Repetition)
		if _, ok := m[k]; ok {
			return nil, fmt.Errorf("duplicate sample %s", k)
		}
		m[k] = s
	}
	return m, nil
}
func WriteComparison(dir string, c Comparison) error {
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	var csvData bytes.Buffer
	w := csv.NewWriter(&csvData)
	_ = w.Write([]string{"case_id", "repetition", "before", "after", "change", "before_ms", "after_ms", "delta_ms", "delta_percent"})
	for _, p := range c.Pairs {
		_ = w.Write([]string{p.CaseID, strconv.Itoa(p.Repetition), p.Before, p.After, p.Change, number(p.BeforeMS), number(p.AfterMS), floatValue(p.DeltaMS), floatValue(p.DeltaPercent)})
	}
	w.Flush()
	if w.Error() != nil {
		return w.Error()
	}
	var mdText strings.Builder
	fmt.Fprintf(&mdText, "# Evaluation comparison\n\n%s → %s\n\n- Regressions: %d\n- Improvements: %d\n- Changed profile fields: %s\n- System prompt changed: %t\n- Matched successful samples: %d\n- Matched latency change: %s%% (negative means faster)\n\nLatency deltas exclude pairs where either sample failed or was skipped. Compare model/engine/quantization/hardware metadata in the original reports before attributing the change. Model tokenizers differ, so tokens/sec alone is not a model-quality or latency ranking.\n\n", md(c.BeforeLabel), md(c.AfterLabel), c.Regressions, c.Improvements, md(strings.Join(c.ChangedProfileFields, ", ")), c.SystemPromptChanged, c.MatchedSuccessfulPairs, floatValue(c.MatchedLatencyChangePercent))
	mdText.WriteString("| Case | Run | Before | After | Change | Delta ms |\n|---|---:|---|---|---|---:|\n")
	for _, p := range c.Pairs {
		fmt.Fprintf(&mdText, "| %s | %d | %s | %s | %s | %s |\n", p.CaseID, p.Repetition, p.Before, p.After, p.Change, floatValue(p.DeltaMS))
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"comparison.json", append(b, '\n')}, {"comparison.csv", csvData.Bytes()}, {"comparison.md", []byte(mdText.String())}} {
		if e := writeNew(filepath.Join(dir, f.name), f.data); e != nil {
			return e
		}
	}
	return nil
}
