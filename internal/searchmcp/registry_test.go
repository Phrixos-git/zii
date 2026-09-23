package searchmcp

import (
	"encoding/json"
	"testing"
)

func validRemoteTools() []Tool {
	inputSchema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	return []Tool{
		{Name: "fetch_page", Description: "Fetch one page", InputSchema: inputSchema},
		{Name: "admin_tool", Description: "Must not be exposed", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "search_web", Description: "Search web", InputSchema: inputSchema},
		{Name: "search_local", Description: "Search local", InputSchema: inputSchema},
	}
}

func TestRegistryAllowlistOrderAndOpenAIConversion(t *testing.T) {
	registry, err := NewRegistry(validRemoteTools())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	tools := registry.Tools()
	if len(tools) != 3 {
		t.Fatalf("registry tools = %d, want 3", len(tools))
	}
	wantNames := []string{"search_local", "search_web", "fetch_page"}
	for i, name := range wantNames {
		if tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Name, name)
		}
	}
	if registry.IsAllowed("admin_tool") || !registry.IsAllowed("search_local") {
		t.Fatal("allowlist membership is incorrect")
	}
	definitions := registry.LLMTools()
	if len(definitions) != 3 || definitions[0].Name != "search_local" || string(definitions[0].Parameters) != string(tools[0].InputSchema) {
		t.Fatalf("LLM tool conversion = %+v", definitions)
	}
	tools[0].InputSchema[0] = 'x'
	if registry.Tools()[0].InputSchema[0] == 'x' {
		t.Fatal("Tools returned shared schema memory")
	}
}

func TestRegistryValidatesArgumentsAgainstCachedSchema(t *testing.T) {
	registry, err := NewRegistry(validRemoteTools())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if err := registry.Validate("search_local", json.RawMessage(`{"query":"text"}`)); err != nil {
		t.Fatalf("Validate valid arguments: %v", err)
	}
	for _, test := range []struct {
		name string
		args json.RawMessage
	}{
		{name: "missing required", args: json.RawMessage(`{}`)},
		{name: "wrong type", args: json.RawMessage(`{"query":1}`)},
		{name: "extra field", args: json.RawMessage(`{"query":"q","extra":true}`)},
		{name: "array", args: json.RawMessage(`[]`)},
		{name: "malformed", args: json.RawMessage(`{bad}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.Validate("search_local", test.args); err == nil {
				t.Fatalf("Validate(%s) succeeded, want error", test.args)
			}
		})
	}
	if err := registry.Validate("admin_tool", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Validate accepted a non-allowlisted tool")
	}
}

func TestRegistryRejectsMissingDuplicateAndInvalidSchemas(t *testing.T) {
	tests := []struct {
		name  string
		tools []Tool
	}{
		{name: "missing", tools: validRemoteTools()[:2]},
		{name: "invalid schema", tools: []Tool{
			{Name: "search_local", InputSchema: json.RawMessage(`{"type":"array"}`)},
			{Name: "search_web", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "fetch_page", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
		{name: "broken schema", tools: []Tool{
			{Name: "search_local", InputSchema: json.RawMessage(`{bad}`)},
			{Name: "search_web", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "fetch_page", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
		{name: "unresolved remote ref", tools: []Tool{
			{Name: "search_local", InputSchema: json.RawMessage(`{"type":"object","$ref":"https://example.invalid/schema.json"}`)},
			{Name: "search_web", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "fetch_page", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
		{name: "duplicate", tools: append(validRemoteTools(), Tool{Name: "search_local", InputSchema: json.RawMessage(`{"type":"object"}`)})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.tools); err == nil {
				t.Fatal("NewRegistry succeeded, want error")
			}
		})
	}
}
