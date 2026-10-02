// Package eval runs repeatable evaluations through Zii's production ToolLoop.
package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Phrixos-git/zii/internal/llm"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Version              int               `yaml:"version" json:"version"`
	Name                 string            `yaml:"name" json:"name"`
	BaseURL              string            `yaml:"base_url" json:"base_url"`
	Model                string            `yaml:"model" json:"model"`
	ModelProfileConfig   string            `yaml:"model_profile_config" json:"model_profile_config,omitempty"`
	ModelProfileID       string            `yaml:"model_profile_id" json:"model_profile_id,omitempty"`
	ResolvedModelProfile *llm.ModelProfile `yaml:"-" json:"resolved_model_profile,omitempty"`
	APIKeyEnv            string            `yaml:"api_key_env" json:"api_key_env,omitempty"`
	Timeout              string            `yaml:"timeout" json:"timeout"`
	MaxTokens            int               `yaml:"max_tokens" json:"max_tokens"`
	Parameters           map[string]any    `yaml:"parameters" json:"parameters,omitempty"`
	OmitParameters       []string          `yaml:"omit_parameters" json:"omit_parameters,omitempty"`
	ToolMode             string            `yaml:"tool_mode" json:"tool_mode"`
	MCPURL               string            `yaml:"mcp_url" json:"mcp_url"`
	MCPTimeout           string            `yaml:"mcp_timeout" json:"mcp_timeout"`
	TokenCounter         string            `yaml:"token_counter" json:"token_counter"`
	MaxToolCalls         int               `yaml:"max_tool_calls" json:"max_tool_calls"`
	SearchLocalMaxCalls  int               `yaml:"search_local_max_calls" json:"search_local_max_calls"`
	SearchWebMaxCalls    int               `yaml:"search_web_max_calls" json:"search_web_max_calls"`
	FetchPageMaxCalls    int               `yaml:"fetch_page_max_calls" json:"fetch_page_max_calls"`
	Environment          map[string]string `yaml:"environment" json:"environment,omitempty"`
}

func DefaultProfile() Profile {
	return Profile{Version: 1, Timeout: "300s", MaxTokens: 4096, ToolMode: "fixture", MCPURL: "http://127.0.0.1:8081/mcp", MCPTimeout: "30s", TokenCounter: "server", MaxToolCalls: 3, SearchLocalMaxCalls: 2, SearchWebMaxCalls: 1, FetchPageMaxCalls: 2}
}

func LoadProfile(path string) (Profile, error) {
	p := DefaultProfile()
	if err := decodeYAML(path, &p); err != nil {
		return p, err
	}
	if p.ModelProfileConfig != "" || p.ModelProfileID != "" {
		if p.ModelProfileConfig == "" || p.ModelProfileID == "" {
			return p, fmt.Errorf("model_profile_config and model_profile_id must be set together")
		}
		configPath := p.ModelProfileConfig
		if !filepath.IsAbs(configPath) {
			configPath = filepath.Join(filepath.Dir(path), configPath)
		}
		profile, err := llm.LoadModelProfile(configPath, p.ModelProfileID)
		if err != nil {
			return p, err
		}
		if (p.Model != "" && p.Model != profile.Model) || (p.BaseURL != "" && p.BaseURL != profile.Endpoint) {
			return p, fmt.Errorf("model/base_url conflict with the selected production model profile")
		}
		p.Model, p.BaseURL = profile.Model, profile.Endpoint
		p.ResolvedModelProfile = &profile
	}
	return p, p.Validate()
}

// Standalone profiles retain the explicit endpoint workflow. Production profile
// references are preferred so capability gating and defaults match the Bot.
func (p Profile) clientConfig() (llm.Config, error) {
	profile := llm.ModelProfile{ID: p.Name, Model: p.Model, Endpoint: p.BaseURL,
		Capabilities:              llm.Capabilities{Tools: true, Reasoning: true, ReasoningContent: true, ReasoningEffort: true, ThinkingBudget: true},
		SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh"},
		Defaults:                  llm.ModelDefaults{ReasoningEffort: "medium"}}
	if p.ResolvedModelProfile != nil {
		profile = *p.ResolvedModelProfile
		profile.Model, profile.Endpoint = p.Model, p.BaseURL
	} else if p.ModelProfileConfig != "" || p.ModelProfileID != "" {
		return llm.Config{}, fmt.Errorf("production model profile must be resolved by LoadProfile")
	}
	cfg := llm.Config{Profile: profile, MaxTokens: p.MaxTokens}
	if v, ok := p.Parameters["reasoning_effort"]; ok {
		effort, valid := v.(string)
		if !valid || strings.TrimSpace(effort) == "" {
			return cfg, fmt.Errorf("reasoning_effort must be a nonempty string")
		}
		if !profile.Capabilities.Reasoning || !profile.Capabilities.ReasoningEffort {
			return cfg, fmt.Errorf("reasoning_effort is disabled by the model profile")
		}
		cfg.ReasoningEffort = effort
	}
	if v, ok := p.Parameters["thinking_budget_tokens"]; ok {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, &cfg.ThinkingBudgetTokens); err != nil || cfg.ThinkingBudgetTokens <= 0 {
			return cfg, fmt.Errorf("thinking_budget_tokens must be a positive integer")
		}
		if !profile.Capabilities.Reasoning || !profile.Capabilities.ThinkingBudget {
			return cfg, fmt.Errorf("thinking_budget_tokens is disabled by the model profile")
		}
	}
	return cfg, nil
}

func (p Profile) Validate() error {
	if _, err := json.Marshal(p); err != nil {
		return fmt.Errorf("profile must be JSON-compatible: %w", err)
	}
	if p.Version != 1 || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("profile requires version: 1, name, and model")
	}
	if err := validateURL(p.BaseURL); err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if strings.HasSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1") {
		return fmt.Errorf("base_url must omit /v1 (the Zii client adds /v1/chat/completions)")
	}
	for _, s := range []string{p.Timeout, p.MCPTimeout} {
		if _, err := positiveDuration(s); err != nil {
			return err
		}
	}
	if p.MaxTokens < 1 || p.MaxToolCalls < 1 || p.SearchLocalMaxCalls < 1 || p.SearchWebMaxCalls < 1 || p.FetchPageMaxCalls < 1 {
		return fmt.Errorf("token and tool limits must be positive")
	}
	if p.ToolMode != "fixture" && p.ToolMode != "live" {
		return fmt.Errorf("tool_mode must be fixture or live")
	}
	if p.TokenCounter != "server" && p.TokenCounter != "estimate" {
		return fmt.Errorf("token_counter must be server or estimate")
	}
	if err := validateURL(p.MCPURL); err != nil {
		return fmt.Errorf("mcp_url: %w", err)
	}
	allowed := map[string]bool{"temperature": true, "top_p": true, "top_k": true, "min_p": true, "seed": true, "reasoning_effort": true, "thinking_budget_tokens": true, "chat_template_kwargs": true}
	for k := range p.Parameters {
		if !allowed[k] {
			return fmt.Errorf("unsupported generation parameter %q", k)
		}
	}
	for _, k := range p.OmitParameters {
		if k != "reasoning_effort" && k != "thinking_budget_tokens" && k != "parallel_tool_calls" {
			return fmt.Errorf("cannot omit request parameter %q", k)
		}
	}
	cfg, err := p.clientConfig()
	if err != nil {
		return err
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return err
	}
	_ = client.Close()
	return nil
}

func validateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("expected HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func positiveDuration(s string) (time.Duration, error) {
	d, e := time.ParseDuration(s)
	if e != nil || d <= 0 {
		return 0, fmt.Errorf("invalid positive duration %q", s)
	}
	return d, nil
}

type History struct {
	Role    string `yaml:"role" json:"role"`
	Content string `yaml:"content" json:"content"`
}
type Expect struct {
	Equals          string   `yaml:"equals" json:"equals,omitempty"`
	Contains        []string `yaml:"contains" json:"contains,omitempty"`
	Pattern         string   `yaml:"pattern" json:"pattern,omitempty"`
	MinToolCalls    int      `yaml:"min_tool_calls" json:"min_tool_calls"`
	MaxToolCalls    *int     `yaml:"max_tool_calls" json:"max_tool_calls,omitempty"`
	RequiredTools   []string `yaml:"required_tools" json:"required_tools,omitempty"`
	ForbiddenTools  []string `yaml:"forbidden_tools" json:"forbidden_tools,omitempty"`
	AllowToolErrors bool     `yaml:"allow_tool_errors" json:"allow_tool_errors"`
	MaxTotalMS      float64  `yaml:"max_total_ms" json:"max_total_ms,omitempty"`
}
type Fixture struct {
	Match   map[string]any `yaml:"match" json:"match,omitempty"`
	IsError bool           `yaml:"is_error" json:"is_error"`
	Data    map[string]any `yaml:"data" json:"data"`
}
type Case struct {
	ID       string               `yaml:"id" json:"id"`
	Category string               `yaml:"category" json:"category"`
	Prompt   string               `yaml:"prompt" json:"prompt"`
	History  []History            `yaml:"history" json:"history,omitempty"`
	Modes    []string             `yaml:"modes" json:"modes,omitempty"`
	Tools    string               `yaml:"tools" json:"tools"`
	Timeout  string               `yaml:"timeout" json:"timeout,omitempty"`
	Expect   Expect               `yaml:"expect" json:"expect"`
	Fixtures map[string][]Fixture `yaml:"fixtures" json:"fixtures,omitempty"`
}
type Suite struct {
	Version int    `yaml:"version" json:"version"`
	Cases   []Case `yaml:"cases" json:"cases"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func LoadSuite(path string) (Suite, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Suite{}, err
	}
	paths := []string{path}
	if info.IsDir() {
		paths = nil
		entries, e := os.ReadDir(path)
		if e != nil {
			return Suite{}, e
		}
		for _, entry := range entries {
			if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml")) {
				paths = append(paths, filepath.Join(path, entry.Name()))
			}
		}
	}
	sort.Strings(paths)
	all := Suite{Version: 1}
	seen := map[string]bool{}
	for _, p := range paths {
		var s Suite
		if err := decodeYAML(p, &s); err != nil {
			return all, err
		}
		if s.Version != 1 {
			return all, fmt.Errorf("%s: unsupported suite version", p)
		}
		for _, c := range s.Cases {
			if c.Tools == "" {
				c.Tools = "auto"
			}
			if err := validateCase(c); err != nil {
				return all, fmt.Errorf("%s: %s: %w", p, c.ID, err)
			}
			if seen[c.ID] {
				return all, fmt.Errorf("duplicate case id %q", c.ID)
			}
			seen[c.ID] = true
			all.Cases = append(all.Cases, c)
		}
	}
	if len(all.Cases) == 0 {
		return all, fmt.Errorf("suite contains no cases")
	}
	return all, nil
}

func validateCase(c Case) error {
	if _, err := json.Marshal(c); err != nil {
		return fmt.Errorf("case must be JSON-compatible: %w", err)
	}
	if !identifier.MatchString(c.ID) || strings.TrimSpace(c.Category) == "" || strings.TrimSpace(c.Prompt) == "" {
		return fmt.Errorf("id, category and prompt are required (id: lowercase letters, digits, _ or -)")
	}
	if c.Tools != "auto" && c.Tools != "none" {
		return fmt.Errorf("tools must be auto or none")
	}
	if c.Timeout != "" {
		if _, e := positiveDuration(c.Timeout); e != nil {
			return e
		}
	}
	if c.Expect.Pattern != "" {
		if _, e := regexp.Compile(c.Expect.Pattern); e != nil {
			return e
		}
	}
	if c.Expect.MinToolCalls < 0 || c.Expect.MaxTotalMS < 0 || (c.Expect.MaxToolCalls != nil && *c.Expect.MaxToolCalls < c.Expect.MinToolCalls) {
		return fmt.Errorf("invalid expectation limits")
	}
	for _, m := range c.Modes {
		if m != "fixture" && m != "live" {
			return fmt.Errorf("unknown mode %q", m)
		}
	}
	if len(c.History)%2 != 0 {
		return fmt.Errorf("history must contain complete user/assistant pairs")
	}
	for i, h := range c.History {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if h.Role != role || strings.TrimSpace(h.Content) == "" {
			return fmt.Errorf("history must alternate nonempty user/assistant messages")
		}
	}
	for name, fs := range c.Fixtures {
		if !toolName(name) {
			return fmt.Errorf("unknown fixture tool %q", name)
		}
		if len(fs) == 0 {
			return fmt.Errorf("empty fixture list")
		}
		for _, f := range fs {
			if f.Data == nil {
				return fmt.Errorf("fixture data must be an object")
			}
		}
	}
	for _, names := range [][]string{c.Expect.RequiredTools, c.Expect.ForbiddenTools} {
		for _, n := range names {
			if !toolName(n) {
				return fmt.Errorf("unknown expected tool %q", n)
			}
		}
	}
	if c.Tools == "none" && (c.Expect.MinToolCalls > 0 || len(c.Expect.RequiredTools) > 0) {
		return fmt.Errorf("tools:none conflicts with required tools")
	}
	return nil
}
func toolName(s string) bool { return s == "search_local" || s == "search_web" || s == "fetch_page" }

func decodeYAML(path string, out any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e = d.Decode(out); e != nil {
		return fmt.Errorf("%s: %w", path, e)
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("%s: expected exactly one YAML document", path)
	}
	return nil
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
