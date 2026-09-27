package discordbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// errorDetails preserves diagnostic context without logging Discord response
// bodies, configured secrets, or the request/reply content supplied by callers.
func (a *Adapter) errorDetails(err error, content ...string) slog.Attr {
	message := err.Error()
	attrs := []any{"type", fmt.Sprintf("%T", err)}
	root := err
	for errors.Unwrap(root) != nil {
		root = errors.Unwrap(root)
	}
	attrs = append(attrs, "cause_type", fmt.Sprintf("%T", root))
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		// RESTError.Error includes the response body, which can echo content.
		message = strings.ReplaceAll(message, rest.Error(), "Discord API request failed")
		if rest.Response != nil {
			attrs = append(attrs, "http_status", rest.Response.StatusCode)
		}
		if rest.Message != nil {
			attrs = append(attrs, "discord_code", rest.Message.Code)
		}
	} else {
		var status interface{ StatusCode() int }
		if errors.As(err, &status) {
			attrs = append(attrs, "http_status", status.StatusCode())
		}
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		attrs = append(attrs, "cause_code", coded.Code())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		attrs = append(attrs, "context_error", "deadline_exceeded")
	} else if errors.Is(err, context.Canceled) {
		attrs = append(attrs, "context_error", "canceled")
	}
	values := append([]string(nil), a.outputGuard.blockedValues...)
	values = append(values, content...)
	// Replace longer values first so overlapping secrets cannot expose suffixes.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	for _, detector := range sensitiveOutputPatterns {
		message = detector.pattern.ReplaceAllString(message, "[redacted]")
	}
	const maxDetailRunes = 2048
	if runes := []rune(message); len(runes) > maxDetailRunes {
		message = string(runes[:maxDetailRunes]) + " [truncated]"
	}
	return slog.Group("error_detail", append(attrs, "message", message)...)
}
