// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	dsnPattern       = regexp.MustCompile(`(?i)((?:postgres(?:ql)?|mysql)://)[^\s]+`)
	mysqlDSNPattern  = regexp.MustCompile(`(?i)([^\s:@/]+:)[^\s]*(@(?:tcp|tcp4|tcp6|unix)\([^)]*\)/[^\s]*)`)
	sqliteURIPattern = regexp.MustCompile(`(?i)(file:)[^\s]+`)
	passwordPattern  = regexp.MustCompile(`(?i)(password\s*[=:]\s*)[^\s,;]+`)
)

func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := dsnPattern.ReplaceAllString(err.Error(), `${1}[redacted]`)
	value = mysqlDSNPattern.ReplaceAllString(value, `${1}[redacted]${2}`)
	value = sqliteURIPattern.ReplaceAllString(value, `${1}[redacted]`)
	value = passwordPattern.ReplaceAllString(value, `${1}[redacted]`)
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		value = value[:500] + "…"
	}
	if value == "" {
		return fmt.Sprintf("%T", err)
	}
	return value
}
