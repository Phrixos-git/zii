package llm

import (
	"regexp"
	"strings"
)

var harmonyHeaderPattern = regexp.MustCompile(`^assistant(?: to=[A-Za-z0-9_.-]+)?(?:<\|channel\|>commentary(?: to=assistant)?)?<\|channel\|>(?:analysis|commentary|final)(?: to=[A-Za-z0-9_.-]+)?(?: (?:<\|constrain\|>)?[A-Za-z0-9_-]+)?$`)

type harmonyHeaderMetadata struct {
	available, valid, recipientPresent, inRequestTools bool
	registered                                         *bool
}

// Inspect only the last assistant header. Never return its text or recipient
// name to the logger. Valid describes the supported header syntax, not JSON
// arguments or the correctness of a tool call.
func inspectLastHarmonyHeader(text string, sentTools []ToolDefinition, registeredNames []string, registryAvailable bool) harmonyHeaderMetadata {
	var result harmonyHeaderMetadata
	if strings.HasPrefix(text, "<|channel|>") {
		text = "<|start|>assistant" + text
	}
	index := strings.LastIndex(text, "<|start|>")
	if index < 0 {
		return result
	}
	header, _, complete := strings.Cut(text[index+len("<|start|>"):], "<|message|>")
	result.available = true
	parts := strings.Split(header, "<|channel|>")
	roleFields := strings.Fields(parts[0])
	var channelFields []string
	if len(parts) > 1 {
		channelFields = strings.Fields(parts[len(parts)-1])
	}
	valid := complete && harmonyHeaderPattern.MatchString(header)
	var extra []string
	if len(roleFields) > 0 {
		extra = append(extra, roleFields[1:]...)
	}
	if len(channelFields) > 0 {
		extra = append(extra, channelFields[1:]...)
	}
	recipient := ""
	for _, field := range extra {
		if strings.HasPrefix(field, "to=") {
			if result.recipientPresent {
				valid = false
			}
			result.recipientPresent = true
			recipient = strings.TrimPrefix(field, "to=")
			if recipient == "" || !harmonyHeaderIdentifier(recipient) {
				valid = false
			}
		}
	}
	result.valid = valid
	// llama.cpp's function recipient uses the functions. namespace. Built-in
	// recipients (e.g. browser) must not accidentally match a registered name.
	name, functionRecipient := strings.CutPrefix(recipient, "functions.")
	for _, tool := range sentTools {
		result.inRequestTools = result.inRequestTools || (functionRecipient && name == tool.Name)
	}
	if registryAvailable {
		matched := false
		for _, registered := range registeredNames {
			matched = matched || (functionRecipient && name == registered)
		}
		result.registered = &matched
	}
	return result
}

func harmonyHeaderIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
