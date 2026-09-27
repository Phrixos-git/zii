package chat

import "regexp"

var toolCallMarkupRegex = regexp.MustCompile(`(?i)<\s*tool_call\b|\bfunction\s*=|\bparameter\s*=`)

// ContainsToolCallMarkup reports whether content resembles leaked tool-call syntax.
func ContainsToolCallMarkup(content string) bool {
	return toolCallMarkupRegex.MatchString(content)
}
