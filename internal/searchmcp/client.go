package searchmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultURL     = "http://127.0.0.1:8081/mcp"
	defaultTimeout = 30 * time.Second
)

// Config controls the reusable Streamable HTTP connection to Search MCP.
type Config struct {
	URL        string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Tool is one cached Search MCP tool description.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolResult carries normalized MCP output. Data prefers structuredContent
// when supplied; otherwise it is the content array. IsError remains explicit.
type ToolResult struct {
	IsError bool
	Data    json.RawMessage
}

// Client reuses one MCP SDK client, Streamable HTTP transport, and session.
type Client struct {
	mu        sync.RWMutex
	mcpClient *mcp.Client
	transport *mcp.StreamableClientTransport
	session   *mcp.ClientSession
	tools     []Tool
	byName    map[string]Tool
}

// NewClient connects to Search MCP and loads the process-local tool cache.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("searchmcp: context is nil")
	}
	endpoint := strings.TrimSpace(cfg.URL)
	if endpoint == "" {
		endpoint = defaultURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("searchmcp: URL must be an absolute http or https URL without user info, query, or fragment")
	}
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("searchmcp: timeout must not be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	} else {
		copy := *httpClient
		if copy.Timeout == 0 || copy.Timeout > timeout {
			copy.Timeout = timeout
		}
		httpClient = &copy
	}
	client := &Client{
		mcpClient: mcp.NewClient(&mcp.Implementation{Name: "zii-search-client", Version: "1.0.0"}, nil),
		transport: &mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClient,
			MaxRetries:           1,
			DisableStandaloneSSE: true,
		},
	}
	session, tools, err := client.connectAndLoad(ctx)
	if err != nil {
		return nil, err
	}
	client.session = session
	client.replaceTools(tools)
	return client, nil
}

// Tools returns a defensive copy of the cached tools in deterministic name order.
func (c *Client) Tools() []Tool {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneTools(c.tools)
}

// Call invokes a cached Search MCP tool using the SDK's tools/call method.
func (c *Client) Call(ctx context.Context, name string, arguments json.RawMessage) (ToolResult, error) {
	if c == nil {
		return ToolResult{}, errors.New("searchmcp: client is nil")
	}
	if ctx == nil {
		return ToolResult{}, errors.New("searchmcp: context is nil")
	}
	if strings.TrimSpace(name) == "" {
		return ToolResult{}, errors.New("searchmcp: tool name is empty")
	}
	var input map[string]json.RawMessage
	if len(arguments) == 0 || json.Unmarshal(arguments, &input) != nil || input == nil {
		return ToolResult{}, errors.New("searchmcp: tool arguments must be a JSON object")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.session == nil {
		return ToolResult{}, errors.New("searchmcp: client is closed")
	}
	if !isAllowedToolName(name) {
		return ToolResult{}, fmt.Errorf("searchmcp: tool %q is outside the allowlist", name)
	}
	if _, ok := c.byName[name]; !ok {
		return ToolResult{}, fmt.Errorf("searchmcp: tool %q is not in the cached tool list", name)
	}
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return ToolResult{}, fmt.Errorf("searchmcp: call tool %q: %w", name, err)
	}
	var data []byte
	if result.StructuredContent != nil {
		data, err = json.Marshal(result.StructuredContent)
	} else {
		data, err = json.Marshal(result.Content)
	}
	if err != nil {
		return ToolResult{}, fmt.Errorf("searchmcp: encode tool %q result: %w", name, err)
	}
	return ToolResult{IsError: result.IsError, Data: json.RawMessage(data)}, nil
}

// Reconnect creates a fresh session and refreshes the cached tool list as one
// atomic replacement. In-flight calls finish before the old session closes.
func (c *Client) Reconnect(ctx context.Context) error {
	if c == nil {
		return errors.New("searchmcp: client is nil")
	}
	if ctx == nil {
		return errors.New("searchmcp: context is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	session, tools, err := c.connectAndLoad(ctx)
	if err != nil {
		return err
	}
	old := c.session
	c.session = session
	c.replaceTools(tools)
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Close closes the cached MCP session. It is safe to call more than once.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
}

func (c *Client) connectAndLoad(ctx context.Context) (*mcp.ClientSession, []Tool, error) {
	session, err := c.mcpClient.Connect(ctx, c.transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("searchmcp: connect: %w", err)
	}
	tools := make([]Tool, 0)
	for remote, iterErr := range session.Tools(ctx, nil) {
		if iterErr != nil {
			_ = session.Close()
			return nil, nil, fmt.Errorf("searchmcp: list tools: %w", iterErr)
		}
		if remote == nil || strings.TrimSpace(remote.Name) == "" {
			_ = session.Close()
			return nil, nil, errors.New("searchmcp: received tool with empty name")
		}
		schema, err := json.Marshal(remote.InputSchema)
		if err != nil || len(schema) == 0 || schema[0] != '{' {
			_ = session.Close()
			return nil, nil, fmt.Errorf("searchmcp: tool %q has invalid input schema", remote.Name)
		}
		tools = append(tools, Tool{Name: remote.Name, Description: remote.Description, InputSchema: schema})
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = session.Close()
		return nil, nil, ctxErr
	}
	return session, tools, nil
}

func (c *Client) replaceTools(tools []Tool) {
	tools = cloneTools(tools)
	byName := make(map[string]Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	c.tools = tools
	c.byName = byName
}

func cloneTools(tools []Tool) []Tool {
	clone := make([]Tool, len(tools))
	for i, tool := range tools {
		clone[i] = tool
		clone[i].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	}
	return clone
}
