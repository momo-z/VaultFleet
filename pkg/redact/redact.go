package redact

import (
	"regexp"
	"strings"
)

const Placeholder = "[REDACTED]"

var sensitiveKV = regexp.MustCompile(`(?i)(\b(?:[a-z0-9_.-]*(?:token|password|passwd|secret|cookie|credential|api_key|access_key|private_key|key_pem)[a-z0-9_.-]*|[a-z0-9_.-]*_key|pass|auth)\b)(\s*[=:]\s*)(\S+)`)
var authorizationToken = regexp.MustCompile(`(?i)(Authorization:\s*(?:Bearer|Basic)\s+)\S+`)

func Text(s string) string {
	s = authorizationToken.ReplaceAllString(s, "${1}"+Placeholder)
	s = sensitiveKV.ReplaceAllString(s, "${1}${2}"+Placeholder)
	return s
}

func JSONFields(m map[string]any, fields ...string) map[string]any {
	redactSet := make(map[string]bool, len(fields))
	for _, field := range fields {
		redactSet[strings.ToLower(field)] = true
	}

	result := make(map[string]any, len(m))
	for key, value := range m {
		if redactSet[strings.ToLower(key)] {
			result[key] = Placeholder
			continue
		}
		result[key] = value
	}
	return result
}
