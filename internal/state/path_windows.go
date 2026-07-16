//go:build windows

package state

import "strings"

func comparePath(value string) string { return strings.ToLower(value) }
