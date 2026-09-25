package discordbot

import (
	"regexp"
	"strings"
)

var sensitiveOutputPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "credential", pattern: regexp.MustCompile(`(?i)\b(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|password|passwd|secret|authorization)\s*[:=]\s*(?:bearer\s+)?[^\s,;}\]]+`)},
	{name: "bearer_token", pattern: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{8,}={0,2}`)},
	{name: "known_token_format", pattern: regexp.MustCompile(`\b(?:AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{16,}|sk-[A-Za-z0-9]{20,})\b`)},
	{name: "jwt", pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)},
	{name: "private_key", pattern: regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{name: "stack_trace", pattern: regexp.MustCompile(`(?i)(?:goroutine\s+\d+\s+\[|panic:|runtime\.goexit|traceback \(most recent call last\)|\.go:\d+(?:\s|$)|^\s*at\s+[\w.$]+\([^)]*:\d+\)|^\s*File "[^"]+", line \d+)`)},
	{name: "internal_path", pattern: regexp.MustCompile(`(?m)(?:^|[\s("'=])/(?:home|root|etc|var|tmp|opt|srv|mnt|run|proc|sys|private|Users|Library|usr/local|workspace|app|data)/[^\s"'<>]+|\b[A-Z]:\\(?:Users|ProgramData|Windows)\\[^\s"'<>]+`)},
	{name: "internal_endpoint", pattern: regexp.MustCompile(`(?i)\bhttps?://(?:localhost|127(?:\.\d{1,3}){3}|0\.0\.0\.0|\[::1\]|10(?:\.\d{1,3}){3}|192\.168(?:\.\d{1,3}){2}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2})(?::\d+)?(?:/[^\s"'<>]*)?`)},
}

// outputGuard blocks exact configured sensitive values and recognizable
// credential, stack trace, local path, and private endpoint disclosures.
type outputGuard struct{ blockedValues []string }

func newOutputGuard(blockedValues []string) outputGuard {
	filtered := make([]string, 0, len(blockedValues))
	for _, value := range blockedValues {
		value = strings.TrimSpace(value)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	return outputGuard{blockedValues: filtered}
}

func (g outputGuard) reason(content string) string {
	for _, value := range g.blockedValues {
		if strings.Contains(content, value) {
			return "configured_sensitive_value"
		}
	}
	for _, detector := range sensitiveOutputPatterns {
		if detector.pattern.MatchString(content) {
			return detector.name
		}
	}
	return ""
}
