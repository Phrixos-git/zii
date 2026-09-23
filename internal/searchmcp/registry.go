package searchmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/google/jsonschema-go/jsonschema"
)

var allowedToolNames = []string{"search_local", "search_web", "fetch_page"}

func isAllowedToolName(name string) bool {
	for _, allowed := range allowedToolNames {
		if name == allowed {
			return true
		}
	}
	return false
}

type registeredTool struct {
	tool   Tool
	schema *jsonschema.Resolved
}

// Registry holds the validated, process-local allowlist of tools exposed to
// the LLM. Tools are emitted in deterministic design order.
type Registry struct {
	byName map[string]registeredTool
	tools  []Tool
}

// NewRegistry validates the cached MCP schemas and includes only the three
// tools approved by OR-06. Missing or duplicate allowlisted tools fail startup.
func NewRegistry(remoteTools []Tool) (*Registry, error) {
	remoteByName := make(map[string]Tool, len(remoteTools))
	for _, tool := range remoteTools {
		if _, exists := remoteByName[tool.Name]; exists {
			return nil, fmt.Errorf("searchmcp: duplicate tool %q in tools/list", tool.Name)
		}
		remoteByName[tool.Name] = tool
	}
	registry := &Registry{byName: make(map[string]registeredTool, len(allowedToolNames))}
	for _, name := range allowedToolNames {
		tool, ok := remoteByName[name]
		if !ok {
			return nil, fmt.Errorf("searchmcp: required allowlisted tool %q is missing", name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("searchmcp: decode input schema for %q: %w", name, err)
		}
		if schema.Type != "object" {
			return nil, fmt.Errorf("searchmcp: input schema for %q must have type object", name)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("searchmcp: resolve input schema for %q: %w", name, err)
		}
		tool.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		registry.tools = append(registry.tools, tool)
		registry.byName[name] = registeredTool{tool: tool, schema: resolved}
	}
	return registry, nil
}

// Tools returns a defensive copy of the allowlisted tools in design order.
func (r *Registry) Tools() []Tool {
	if r == nil {
		return nil
	}
	return cloneTools(r.tools)
}

// LLMTools converts the allowlist to OpenAI Function Tool definitions.
func (r *Registry) LLMTools() []llm.ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, llm.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  append(json.RawMessage(nil), tool.InputSchema...),
		})
	}
	return definitions
}

// Validate checks raw LLM arguments against the cached tool JSON Schema.
func (r *Registry) Validate(name string, arguments json.RawMessage) error {
	if r == nil {
		return errors.New("searchmcp: tool registry is nil")
	}
	tool, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("searchmcp: tool %q is not allowlisted", name)
	}
	var instance map[string]any
	if len(arguments) == 0 || json.Unmarshal(arguments, &instance) != nil || instance == nil {
		return errors.New("searchmcp: tool arguments must be a JSON object")
	}
	if err := tool.schema.Validate(instance); err != nil {
		return fmt.Errorf("searchmcp: arguments do not match schema for %q: %w", name, err)
	}
	return nil
}

// IsAllowed reports whether a tool name is part of this registry.
func (r *Registry) IsAllowed(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.byName[strings.TrimSpace(name)]
	return ok
}
