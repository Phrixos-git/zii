package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/searchmcp"
)

type RunOptions struct {
	Runs        int    `json:"runs"`
	Warmup      int    `json:"warmup"`
	Category    string `json:"category,omitempty"`
	ID          string `json:"id,omitempty"`
	SaveAnswers bool   `json:"save_answers"`
	Label       string `json:"label"`
}
type Sample struct {
	SystemPromptHash string   `json:"system_prompt_sha256,omitempty"`
	CaseID           string   `json:"case_id"`
	Category         string   `json:"category"`
	Repetition       int      `json:"repetition"`
	Warmup           bool     `json:"warmup"`
	Status           string   `json:"status"`
	SkipReason       string   `json:"skip_reason,omitempty"`
	FinalContent     bool     `json:"final_content"`
	FinalAnswer      string   `json:"final_answer,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	Failures         []string `json:"failures,omitempty"`
	Anomalies        []string `json:"anomalies,omitempty"`
	Metrics          Metrics  `json:"metrics"`
	Trace            Trace    `json:"trace"`
}
type Report struct {
	Version          int               `json:"version"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
	Complete         bool              `json:"complete"`
	SetupError       string            `json:"setup_error,omitempty"`
	Profile          Profile           `json:"profile"`
	Options          RunOptions        `json:"options"`
	Suite            Suite             `json:"suite"`
	SuiteHash        string            `json:"suite_sha256"`
	ToolSchemaHash   string            `json:"tool_schema_sha256"`
	SystemPromptHash string            `json:"system_prompt_sha256"`
	ProtocolHash     string            `json:"protocol_sha256"`
	Source           map[string]string `json:"source"`
	Samples          []Sample          `json:"samples"`
	Summary          Summary           `json:"summary"`
}

// Run executes sequentially, with a fresh ToolLoop/fixture state per sample.
// It never opens Discord or the production conversation database.
func Run(ctx context.Context, p Profile, s Suite, o RunOptions, progress func(Sample)) (report Report, err error) {
	if cfg, e := p.clientConfig(); e == nil {
		p.ResolvedModelProfile = &cfg.Profile
	}
	report = Report{Version: 1, StartedAt: time.Now().UTC(), Profile: p, Options: o, Suite: s, SuiteHash: hash(s), SystemPromptHash: hash(orchestrator.DefaultSystemPrompt()), Source: sourceInfo()}
	defer func() { report.FinishedAt = time.Now().UTC(); report.Summary = summarize(report.Samples) }()
	if e := p.Validate(); e != nil {
		return report, e
	}
	if o.Runs < 1 || o.Warmup < 0 {
		return report, fmt.Errorf("runs must be positive and warmup nonnegative")
	}
	apiKey := ""
	if p.APIKeyEnv != "" {
		apiKey = os.Getenv(p.APIKeyEnv)
		if apiKey == "" {
			report.SetupError = "api_key_env_empty"
			return report, fmt.Errorf("api_key_env %q is not set", p.APIKeyEnv)
		}
	}
	selected := []Case{}
	for _, c := range s.Cases {
		if (o.ID == "" || c.ID == o.ID) && (o.Category == "" || c.Category == o.Category) {
			selected = append(selected, c)
		}
	}
	if len(selected) == 0 {
		return report, fmt.Errorf("no cases match the selected id/category")
	}
	eligible := false
	for _, c := range selected {
		if supports(c, p.ToolMode) {
			eligible = true
		}
	}
	if !eligible {
		return report, fmt.Errorf("no cases support tool_mode %s", p.ToolMode)
	}
	tools := FixtureTools()
	var live *searchmcp.Client
	needsLive := false
	for _, c := range selected {
		if supports(c, p.ToolMode) && c.Tools != "none" {
			needsLive = true
		}
	}
	if p.ToolMode == "live" && needsLive {
		timeout, _ := positiveDuration(p.MCPTimeout)
		setup, cancel := context.WithTimeout(ctx, timeout)
		live, err = searchmcp.NewClient(setup, searchmcp.Config{URL: p.MCPURL, Timeout: timeout})
		cancel()
		if err != nil {
			report.SetupError = "mcp_setup_error"
			return report, errors.New("Search MCP initialization failed (check endpoint and tools/list)")
		}
		defer live.Close()
		tools = live.Tools()
	}
	registry, err := searchmcp.NewRegistry(tools)
	if err != nil {
		report.SetupError = "tool_schema_error"
		return report, errors.New("Search MCP tool registry is invalid")
	}
	report.ToolSchemaHash = hash(registry.Tools())
	report.SystemPromptHash = hash(orchestrator.BuildSystemPrompt(*p.ResolvedModelProfile, registry.LLMTools()))
	report.ProtocolHash = hash(struct {
		Mode, Counter, Timeout, MCPTimeout   string
		Max, Local, Web, Fetch, Runs, Warmup int
		Category, ID, Schema                 string
	}{p.ToolMode, p.TokenCounter, p.Timeout, p.MCPTimeout, p.MaxToolCalls, p.SearchLocalMaxCalls, p.SearchWebMaxCalls, p.FetchPageMaxCalls, o.Runs, o.Warmup, o.Category, o.ID, report.ToolSchemaHash})
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	for phase := 0; phase < 2; phase++ {
		n := o.Warmup
		if phase == 1 {
			n = o.Runs
		}
		for rep := 1; rep <= n; rep++ {
			for _, c := range selected {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				if !supports(c, p.ToolMode) {
					if phase == 1 {
						sample := Sample{CaseID: c.ID, Category: c.Category, Repetition: rep, Status: "SKIP", SkipReason: "requires another tool mode"}
						report.Samples = append(report.Samples, sample)
						if progress != nil {
							progress(sample)
						}
					}
					continue
				}
				sample := runSample(ctx, p, c, registry, live, base, apiKey, o.SaveAnswers)
				sample.Repetition = rep
				sample.Warmup = phase == 0
				report.Samples = append(report.Samples, sample)
				if progress != nil {
					progress(sample)
				}
			}
		}
	}
	report.Complete = true
	return report, nil
}
func supports(c Case, mode string) bool {
	if len(c.Modes) == 0 {
		return true
	}
	for _, m := range c.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

func runSample(parent context.Context, p Profile, c Case, registry *searchmcp.Registry, live *searchmcp.Client, base http.RoundTripper, key string, save bool) Sample {
	s := Sample{CaseID: c.ID, Category: c.Category}
	trace := Trace{}
	deadline := p.Timeout
	if c.Timeout != "" {
		deadline = c.Timeout
	}
	duration, _ := positiveDuration(deadline)
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	reg := caseRegistry{Registry: registry, enabled: c.Tools != "none"}
	transport := &observedTransport{base: base, profile: p, apiKey: key, trace: &trace, registry: reg, seenIDs: map[string]bool{}}
	cfg, e := p.clientConfig()
	if e != nil {
		s.Status, s.ErrorCode = "FAIL", "client_config_error"
		return s
	}
	cfg.Timeout = duration
	cfg.HTTPClient = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client, e := llm.NewClient(cfg)
	if e != nil {
		s.Status = "FAIL"
		s.ErrorCode = "client_config_error"
		return s
	}
	observed := &observedChat{client: client, trace: &trace, counter: p.TokenCounter}
	var search orchestrator.SearchToolClient = &fixtureSearch{fixtures: c.Fixtures, index: map[string]int{}}
	if live != nil {
		search = live
	}
	loop, e := orchestrator.NewToolLoopWithConfig(observed, &observedSearch{inner: search, trace: &trace}, reg, orchestrator.ToolLoopConfig{MaxCalls: p.MaxToolCalls, SearchLocalMaxCalls: p.SearchLocalMaxCalls, SearchWebMaxCalls: p.SearchWebMaxCalls, FetchPageMaxCalls: p.FetchPageMaxCalls, LLMMaxConcurrency: 1, MCPMaxConcurrency: 1})
	if e != nil {
		s.Status = "FAIL"
		s.ErrorCode = "loop_config_error"
		return s
	}
	prompt := orchestrator.BuildSystemPrompt(client.ModelProfile(), reg.LLMTools())
	s.SystemPromptHash = hash(prompt)
	messages := []chat.Message{{Role: "system", Content: prompt}}
	for _, h := range c.History {
		messages = append(messages, chat.Message{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, chat.Message{Role: "user", Content: c.Prompt})
	start := time.Now()
	answer, runErr := loop.Run(ctx, messages)
	s.Trace = trace
	s.Metrics = measure(trace, float64(time.Since(start))/float64(time.Millisecond))
	s.FinalContent = strings.TrimSpace(answer) != ""
	validateSample(&s, c, p, answer, runErr)
	if save {
		s.FinalAnswer = answer
	}
	return s
}

type caseRegistry struct {
	*searchmcp.Registry
	enabled bool
}

func (r caseRegistry) IsAllowed(name string) bool { return r.enabled && r.Registry.IsAllowed(name) }
func (r caseRegistry) LLMTools() []llm.ToolDefinition {
	if !r.enabled {
		return nil
	}
	return r.Registry.LLMTools()
}

type observedChat struct {
	client  *llm.Client
	trace   *Trace
	counter string
}

func (c *observedChat) Chat(ctx context.Context, m []chat.Message, t []llm.ToolDefinition) (llm.Completion, error) {
	return c.chat(ctx, m, t, llm.ChatOptions{})
}
func (c *observedChat) ChatWithOptions(ctx context.Context, m []chat.Message, t []llm.ToolDefinition, o llm.ChatOptions) (llm.Completion, error) {
	// ToolChoice:none alone also marks the first normal answer after the tool
	// budget is exhausted. Only an output-recovery override counts as a retry.
	if o.MaxTokens > 0 || o.ReasoningEffort != "" || o.ThinkingBudgetTokens > 0 {
		c.trace.FinalizationRetries++
	}
	return c.chat(ctx, m, t, o)
}
func (c *observedChat) ModelProfile() llm.ModelProfile { return c.client.ModelProfile() }
func (c *observedChat) chat(ctx context.Context, m []chat.Message, t []llm.ToolDefinition, o llm.ChatOptions) (llm.Completion, error) {
	c.trace.LLMCalls++
	ctx = context.WithValue(ctx, metaKey{}, requestMeta{Kind: "completion", Call: c.trace.LLMCalls})
	return c.client.ChatWithOptions(ctx, m, t, o)
}
func (c *observedChat) CountTokens(ctx context.Context, m []chat.Message) (int, error) {
	c.trace.TokenCountCalls++
	if c.counter == "estimate" {
		if e := ctx.Err(); e != nil {
			return 0, e
		}
		b, e := json.Marshal(m)
		return utf8.RuneCount(b), e
	}
	ctx = context.WithValue(ctx, metaKey{}, requestMeta{Kind: "token_count", Call: c.trace.TokenCountCalls})
	return c.client.CountTokens(ctx, m)
}

func sourceInfo() map[string]string {
	s := map[string]string{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
	if b, ok := debug.ReadBuildInfo(); ok {
		for _, v := range b.Settings {
			if strings.HasPrefix(v.Key, "vcs.") {
				s["build_"+v.Key] = v.Value
			}
		}
	}
	command := func(args ...string) string {
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	s["worktree_revision"] = command("rev-parse", "HEAD")
	s["worktree_dirty"] = fmt.Sprint(command("status", "--porcelain") != "")
	paths := strings.Split(command("ls-files", "-c", "-o", "--exclude-standard", "-z"), "\x00")
	sort.Strings(paths)
	files := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, ".go") || path == "go.mod" || path == "go.sum" {
			if b, e := os.ReadFile(path); e == nil {
				files[path] = hash(string(b))
			}
		}
	}
	if len(files) > 0 {
		s["worktree_source_sha256"] = hash(files)
	}
	return s
}
