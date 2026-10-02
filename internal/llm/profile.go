package llm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Capabilities describes only the model behaviors used by Zii.
type Capabilities struct {
	Tools             bool `yaml:"tools"`
	Reasoning         bool `yaml:"reasoning"`
	ReasoningContent  bool `yaml:"reasoning_content"`
	ReasoningEffort   bool `yaml:"reasoning_effort"`
	ThinkingBudget    bool `yaml:"thinking_budget"`
	ParallelToolCalls bool `yaml:"parallel_tool_calls"`
}

// ModelDefaults contains runtime generation defaults declared by a profile.
// A nil ThinkingBudgetTokens means that the profile has no budget default.
type ModelDefaults struct {
	ReasoningEffort      string
	ThinkingBudgetTokens *int
}

// ModelProfile separates the selected profile key, model identifier, endpoint,
// supported settings, and runtime defaults.
type ModelProfile struct {
	ID                        string
	Model                     string
	Endpoint                  string
	Capabilities              Capabilities
	SupportedReasoningEfforts []string
	Defaults                  ModelDefaults
}

type modelProfilesDocument struct {
	Models map[string]modelProfileYAML `yaml:"models"`
}

type modelProfileYAML struct {
	Model                     string             `yaml:"model"`
	Endpoint                  string             `yaml:"endpoint"`
	Capabilities              capabilitiesYAML   `yaml:"capabilities"`
	SupportedReasoningEfforts *[]string          `yaml:"supported_reasoning_efforts"`
	Defaults                  *modelDefaultsYAML `yaml:"defaults"`
}

type capabilitiesYAML struct {
	Tools             *bool `yaml:"tools"`
	Reasoning         *bool `yaml:"reasoning"`
	ReasoningContent  *bool `yaml:"reasoning_content"`
	ReasoningEffort   *bool `yaml:"reasoning_effort"`
	ThinkingBudget    *bool `yaml:"thinking_budget"`
	ParallelToolCalls *bool `yaml:"parallel_tool_calls"`
}

type modelDefaultsYAML struct {
	ReasoningEffort      *string   `yaml:"reasoning_effort"`
	ThinkingBudgetTokens yaml.Node `yaml:"thinking_budget_tokens"`
}

// LoadModelProfiles parses and validates all profiles in a YAML file. Unknown
// fields, missing fields, malformed YAML, and invalid values are errors.
func LoadModelProfiles(path string) (map[string]ModelProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("llm: read model profile config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document modelProfilesDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("llm: decode model profile config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("llm: model profile config must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("llm: decode model profile config: %w", err)
	}
	if len(document.Models) == 0 {
		return nil, fmt.Errorf("llm: model profile config must define at least one model")
	}
	profiles := make(map[string]ModelProfile, len(document.Models))
	for id, raw := range document.Models {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("llm: model profile key must be non-empty and have no surrounding whitespace")
		}
		profile, err := raw.toProfile(id)
		if err != nil {
			return nil, fmt.Errorf("llm: model profile %q: %w", id, err)
		}
		profiles[id] = profile
	}
	return profiles, nil
}

// LoadModelProfile loads the file and returns the exact profile ID requested.
func LoadModelProfile(path, id string) (ModelProfile, error) {
	if strings.TrimSpace(id) == "" {
		return ModelProfile{}, fmt.Errorf("llm: model profile ID must not be empty")
	}
	profiles, err := LoadModelProfiles(path)
	if err != nil {
		return ModelProfile{}, err
	}
	profile, ok := profiles[id]
	if !ok {
		return ModelProfile{}, fmt.Errorf("llm: model profile %q is not configured", id)
	}
	return profile, nil
}

func (raw modelProfileYAML) toProfile(id string) (ModelProfile, error) {
	if raw.Model == "" || strings.TrimSpace(raw.Model) != raw.Model {
		return ModelProfile{}, fmt.Errorf("model must be set without surrounding whitespace")
	}
	endpoint := raw.Endpoint
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint {
		return ModelProfile{}, fmt.Errorf("endpoint must be set without surrounding whitespace")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ModelProfile{}, fmt.Errorf("endpoint must be an absolute http or https URL without credentials, query, or fragment")
	}
	capabilities, err := raw.Capabilities.toCapabilities()
	if err != nil {
		return ModelProfile{}, err
	}
	if raw.SupportedReasoningEfforts == nil {
		return ModelProfile{}, fmt.Errorf("supported_reasoning_efforts must be set, even when empty")
	}
	if raw.Defaults == nil || raw.Defaults.ReasoningEffort == nil || raw.Defaults.ThinkingBudgetTokens.Kind == 0 {
		return ModelProfile{}, fmt.Errorf("defaults.reasoning_effort and defaults.thinking_budget_tokens must both be set")
	}
	efforts := append([]string(nil), (*raw.SupportedReasoningEfforts)...)
	seenEfforts := make(map[string]struct{}, len(efforts))
	for _, effort := range efforts {
		if effort == "" || strings.TrimSpace(effort) != effort {
			return ModelProfile{}, fmt.Errorf("supported reasoning efforts must be non-empty and have no surrounding whitespace")
		}
		if _, duplicate := seenEfforts[effort]; duplicate {
			return ModelProfile{}, fmt.Errorf("supported reasoning effort %q is duplicated", effort)
		}
		seenEfforts[effort] = struct{}{}
	}
	defaultEffort := *raw.Defaults.ReasoningEffort
	if defaultEffort != "" && strings.TrimSpace(defaultEffort) != defaultEffort {
		return ModelProfile{}, fmt.Errorf("defaults.reasoning_effort must not have surrounding whitespace")
	}
	if capabilities.ReasoningEffort {
		if len(efforts) == 0 {
			return ModelProfile{}, fmt.Errorf("supported_reasoning_efforts must not be empty when reasoning_effort is enabled")
		}
		if defaultEffort == "" {
			return ModelProfile{}, fmt.Errorf("defaults.reasoning_effort is required when reasoning_effort is enabled")
		}
		if _, ok := seenEfforts[defaultEffort]; !ok {
			return ModelProfile{}, fmt.Errorf("default reasoning effort %q is not listed as supported", defaultEffort)
		}
	} else if len(efforts) != 0 || defaultEffort != "" {
		return ModelProfile{}, fmt.Errorf("reasoning effort values require the reasoning_effort capability")
	}
	var budget *int
	if raw.Defaults.ThinkingBudgetTokens.Tag != "!!null" {
		var value int
		if err := raw.Defaults.ThinkingBudgetTokens.Decode(&value); err != nil {
			return ModelProfile{}, fmt.Errorf("defaults.thinking_budget_tokens must be an integer or null: %w", err)
		}
		if value <= 0 {
			return ModelProfile{}, fmt.Errorf("defaults.thinking_budget_tokens must be positive or null")
		}
		budget = &value
	}
	if budget != nil && !capabilities.ThinkingBudget {
		return ModelProfile{}, fmt.Errorf("a thinking budget default requires the thinking_budget capability")
	}
	profile := ModelProfile{
		ID:                        id,
		Model:                     raw.Model,
		Endpoint:                  endpoint,
		Capabilities:              capabilities,
		SupportedReasoningEfforts: efforts,
		Defaults:                  ModelDefaults{ReasoningEffort: defaultEffort, ThinkingBudgetTokens: budget},
	}
	if err := validateModelProfile(profile); err != nil {
		return ModelProfile{}, err
	}
	return profile, nil
}

func (raw capabilitiesYAML) toCapabilities() (Capabilities, error) {
	values := []*bool{raw.Tools, raw.Reasoning, raw.ReasoningContent, raw.ReasoningEffort, raw.ThinkingBudget, raw.ParallelToolCalls}
	for _, value := range values {
		if value == nil {
			return Capabilities{}, fmt.Errorf("all six capability fields must be explicitly set")
		}
	}
	return Capabilities{
		Tools:             *raw.Tools,
		Reasoning:         *raw.Reasoning,
		ReasoningContent:  *raw.ReasoningContent,
		ReasoningEffort:   *raw.ReasoningEffort,
		ThinkingBudget:    *raw.ThinkingBudget,
		ParallelToolCalls: *raw.ParallelToolCalls,
	}, nil
}

func validateModelProfile(profile ModelProfile) error {
	if profile.ID == "" || strings.TrimSpace(profile.ID) != profile.ID {
		return fmt.Errorf("profile ID must be set without surrounding whitespace")
	}
	if profile.Model == "" || strings.TrimSpace(profile.Model) != profile.Model {
		return fmt.Errorf("profile model must be set without surrounding whitespace")
	}
	if profile.Endpoint == "" || strings.TrimSpace(profile.Endpoint) != profile.Endpoint {
		return fmt.Errorf("profile endpoint must be set without surrounding whitespace")
	}
	_, err := normalizeBaseURL(profile.Endpoint)
	if err != nil {
		return fmt.Errorf("profile endpoint: %w", err)
	}
	if profile.Capabilities.ReasoningEffort {
		if len(profile.SupportedReasoningEfforts) == 0 || profile.Defaults.ReasoningEffort == "" {
			return fmt.Errorf("reasoning effort capability requires supported values and a default")
		}
		found := false
		for _, effort := range profile.SupportedReasoningEfforts {
			if effort == profile.Defaults.ReasoningEffort {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("default reasoning effort %q is not supported", profile.Defaults.ReasoningEffort)
		}
	} else if len(profile.SupportedReasoningEfforts) != 0 || profile.Defaults.ReasoningEffort != "" {
		return fmt.Errorf("reasoning effort values require the reasoning_effort capability")
	}
	if budget := profile.Defaults.ThinkingBudgetTokens; budget != nil {
		if *budget <= 0 {
			return fmt.Errorf("default thinking budget must be positive")
		}
		if !profile.Capabilities.ThinkingBudget {
			return fmt.Errorf("thinking budget default requires the thinking_budget capability")
		}
	}
	return nil
}
