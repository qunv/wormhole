//go:build !windows

package state

func comparePath(value string) string { return value }
