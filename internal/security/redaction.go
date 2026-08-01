// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	auditRedact  = regexp.MustCompile(`(?i)^(content|body|diff|patch|old_text|new_text|command|sql|query|params|parameters|rows|result|dsn|database[_-]?url|connection[_-]?string|credential[_-]?ref|token|approval_token|mcp_auth_token|control_plane_api_key|key|secret|password|authorization|auth|api[_-]?key)$`)
	bearerSecret = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	urlUserInfo  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	inlineSecret = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|authorization|credential)\s*[:=]\s*[^\s,;]+`)
)

func RedactDeep(value any, depth int) any {
	if depth > 8 {
		return "…"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		count := 0
		for key, nested := range typed {
			if count >= 50 {
				break
			}
			if auditRedact.MatchString(key) {
				if text, ok := nested.(string); ok {
					out[key] = fmt.Sprintf("[redacted %d chars]", len(text))
				} else {
					out[key] = "[redacted]"
				}
			} else {
				out[key] = RedactDeep(nested, depth+1)
			}
			count++
		}
		return out
	case []any:
		limit := min(50, len(typed))
		out := make([]any, limit)
		for i := 0; i < limit; i++ {
			out[i] = RedactDeep(typed[i], depth+1)
		}
		return out
	case string:
		if len(typed) > 200 {
			return fmt.Sprintf("%s…(%d chars)", typed[:200], len(typed))
		}
		return typed
	default:
		return value
	}
}

// RedactText removes common credential forms from unstructured diagnostic text
// and returns a valid UTF-8 string bounded by maxBytes. It is intended for
// errors and logs where key-aware RedactDeep cannot be applied.
func RedactText(value string, maxBytes int) string {
	value = bearerSecret.ReplaceAllString(value, "Bearer [redacted]")
	value = urlUserInfo.ReplaceAllString(value, "$1[redacted]@")
	value = inlineSecret.ReplaceAllStringFunc(value, func(match string) string {
		name, _, ok := strings.Cut(match, ":")
		if !ok {
			name, _, _ = strings.Cut(match, "=")
		}
		return strings.TrimSpace(name) + "=[redacted]"
	})
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value + "…"
}

func SummarizeArgs(args map[string]any) string {
	raw, err := json.Marshal(RedactDeep(args, 0))
	if err != nil {
		return "<unserializable>"
	}
	if len(raw) > 800 {
		return string(raw[:800]) + "…"
	}
	return string(raw)
}
