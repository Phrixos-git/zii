package chat

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ToolCall is an OpenAI-compatible function call requested by the assistant.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall identifies a function and preserves its arguments as supplied
// by the LLM endpoint. Arguments may be encoded as a JSON string or object.
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// NormalizedArguments returns the function arguments as a JSON object. Some
// compatible endpoints encode arguments as a JSON string while others return
// the object directly.
func (f FunctionCall) NormalizedArguments() (json.RawMessage, error) {
	input := bytes.TrimSpace(f.Arguments)
	if len(input) == 0 {
		return nil, errors.New("chat: function arguments are empty")
	}

	var encoded string
	if err := json.Unmarshal(input, &encoded); err == nil {
		input = bytes.TrimSpace([]byte(encoded))
	}
	if len(input) == 0 || input[0] != '{' {
		return nil, errors.New("chat: function arguments must be a JSON object")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil {
		return nil, errors.New("chat: function arguments contain invalid JSON object")
	}
	if object == nil {
		return nil, errors.New("chat: function arguments must be a JSON object")
	}
	return append(json.RawMessage(nil), input...), nil
}
