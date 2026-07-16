// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var auditRedact = regexp.MustCompile(`(?i)^(content|body|diff|patch|old_text|new_text|command|token|approval_token|mcp_auth_token|control_plane_api_key|key|secret|password|authorization|auth|api[_-]?key)$`)

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
