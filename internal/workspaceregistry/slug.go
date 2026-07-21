// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspaceregistry

import "strings"

// SlugID converts a repository or folder name into a stable workspace ID.
// Generated IDs contain lowercase ASCII letters, digits, and hyphens only.
// Unsupported character runs collapse into a single hyphen.
func SlugID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	separator := false
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(char)
			separator = false
			continue
		}
		separator = builder.Len() > 0
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "workspace"
	}
	if len(result) > 32 {
		result = strings.TrimRight(result[:32], "-")
	}
	if result == "" {
		return "workspace"
	}
	return result
}
