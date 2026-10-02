package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validProfileYAML = `models:
  qwen:
    model: qwen-server-id
    endpoint: http://127.0.0.1:8080
    capabilities:
      tools: true
      reasoning: true
      reasoning_content: true
      reasoning_effort: true
      thinking_budget: true
      parallel_tool_calls: true
    supported_reasoning_efforts: [medium, high]
    defaults:
      reasoning_effort: medium
      thinking_budget_tokens: 2048
  gpt-oss-20b:
    model: gpt-oss-20b
    endpoint: http://127.0.0.1:8082
    capabilities:
      tools: true
      reasoning: true
      reasoning_content: true
      reasoning_effort: true
      thinking_budget: true
      parallel_tool_calls: false
    supported_reasoning_efforts: [low, medium, high]
    defaults:
      reasoning_effort: medium
      thinking_budget_tokens: null
`

func writeProfileYAML(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadModelProfilesSupportsMultipleProfilesAndNullBudget(t *testing.T) {
	profiles, err := LoadModelProfiles(writeProfileYAML(t, validProfileYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("loaded profiles = %d, want 2", len(profiles))
	}
	qwen := profiles["qwen"]
	if qwen.ID != "qwen" || qwen.Model != "qwen-server-id" || qwen.Endpoint != "http://127.0.0.1:8080" || !qwen.Capabilities.ParallelToolCalls || qwen.Defaults.ReasoningEffort != "medium" || qwen.Defaults.ThinkingBudgetTokens == nil || *qwen.Defaults.ThinkingBudgetTokens != 2048 {
		t.Fatalf("Qwen profile = %+v", qwen)
	}
	gpt := profiles["gpt-oss-20b"]
	if gpt.Model != "gpt-oss-20b" || gpt.Capabilities.ParallelToolCalls || gpt.Defaults.ThinkingBudgetTokens != nil {
		t.Fatalf("gpt-oss profile = %+v", gpt)
	}
	selected, err := LoadModelProfile(writeProfileYAML(t, validProfileYAML), "gpt-oss-20b")
	if err != nil || selected.ID != "gpt-oss-20b" {
		t.Fatalf("LoadModelProfile = %+v, %v", selected, err)
	}
}

func TestLoadModelProfileRejectsUnknownID(t *testing.T) {
	_, err := LoadModelProfile(writeProfileYAML(t, validProfileYAML), "missing-model")
	if err == nil || !strings.Contains(err.Error(), `model profile "missing-model" is not configured`) {
		t.Fatalf("unknown profile error = %v", err)
	}
}

func TestRepositoryProfileFilesAreValidAndSelectDifferentModels(t *testing.T) {
	profiles, err := LoadModelProfiles(filepath.Join("..", "..", "config", "model_profiles.yaml"))
	if err != nil {
		t.Fatalf("load runtime profiles: %v", err)
	}
	qwen, ok := profiles["qwen"]
	if !ok || qwen.Model != "qwen" || qwen.Endpoint != "http://127.0.0.1:8080" {
		t.Fatalf("runtime Qwen profile = %+v, found=%t", qwen, ok)
	}
	gptProfiles, err := LoadModelProfiles(filepath.Join("..", "..", "config", "gpt-oss-20b.profile.example.yaml"))
	if err != nil {
		t.Fatalf("load gpt-oss profile example: %v", err)
	}
	if gptProfiles["gpt-oss-20b"].Model != "gpt-oss-20b" {
		t.Fatalf("gpt-oss profile example = %+v", gptProfiles["gpt-oss-20b"])
	}
}

func TestLoadModelProfilesRejectsMalformedOrIncompleteConfiguration(t *testing.T) {
	missingCapability := strings.Replace(validProfileYAML, "      tools: true\n", "", 1)
	missingDefaultsBudget := strings.Replace(validProfileYAML, "      thinking_budget_tokens: 2048\n", "", 1)
	unsupportedDefault := strings.Replace(validProfileYAML, "      reasoning_effort: medium\n", "      reasoning_effort: none\n", 1)
	unsupportedFields := strings.Replace(validProfileYAML, "    model: qwen-server-id\n", "    unknown_setting: true\n    model: qwen-server-id\n", 1)
	missingEndpoint := strings.Replace(validProfileYAML, "    endpoint: http://127.0.0.1:8080\n", "", 1)
	badBudget := strings.Replace(validProfileYAML, "      thinking_budget_tokens: 2048\n", "      thinking_budget_tokens: 0\n", 1)
	duplicateProfile := validProfileYAML + "  qwen:\n    model: duplicate\n"
	secondDocument := validProfileYAML + "---\nmodels: {}\n"
	for _, tt := range []struct {
		name string
		data string
	}{
		{name: "malformed yaml", data: "models: ["},
		{name: "empty models", data: "models: {}\n"},
		{name: "unknown field", data: unsupportedFields},
		{name: "missing endpoint", data: missingEndpoint},
		{name: "missing capability", data: missingCapability},
		{name: "missing default key", data: missingDefaultsBudget},
		{name: "unsupported default effort", data: unsupportedDefault},
		{name: "zero budget", data: badBudget},
		{name: "duplicate profile key", data: duplicateProfile},
		{name: "multiple yaml documents", data: secondDocument},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadModelProfiles(writeProfileYAML(t, tt.data)); err == nil {
				t.Fatal("LoadModelProfiles succeeded, want configuration error")
			}
		})
	}
}
