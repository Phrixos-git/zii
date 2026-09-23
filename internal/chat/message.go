// Package chat defines shared chat message types used by the LLM client and
// context builder.
package chat

// Message is one role-preserving chat message exchanged with the LLM.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
