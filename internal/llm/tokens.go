package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Phrixos-git/zii/internal/chat"
)

// CountTokens asks llama.cpp to count the supplied messages using its active
// model and chat template. It never falls back to an approximation.
func (c *Client) CountTokens(ctx context.Context, messages []chat.Message) (int, error) {
	if c == nil {
		return 0, errors.New("llm: client is nil")
	}
	if ctx == nil {
		return 0, errors.New("llm: context is nil")
	}
	body, err := json.Marshal(struct {
		Model    string         `json:"model"`
		Messages []chat.Message `json:"messages"`
	}{Model: c.model, Messages: messages})
	if err != nil {
		return 0, fmt.Errorf("llm: encode token-count request: %w", err)
	}
	responseBody, err := c.postJSON(ctx, "/v1/chat/completions/input_tokens", body)
	if err != nil {
		return 0, err
	}
	var response struct {
		InputTokens *int `json:"input_tokens"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return 0, fmt.Errorf("llm: decode token-count response: %w", err)
	}
	if response.InputTokens == nil || *response.InputTokens < 0 {
		return 0, fmt.Errorf("llm: token-count response is missing a valid input_tokens value")
	}
	return *response.InputTokens, nil
}
