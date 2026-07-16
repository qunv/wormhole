// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"os"

	"codebridge/internal/cli"
)

const (
	Name = "Codebridge"
	Tier = "pro"
)

var Version = "0.1.0-dev"

// Run is the composition root for the native CLI and its internal MCP server.
func Run(ctx context.Context, args []string) error {
	return (cli.App{
		Name: Name, Version: Version, Tier: Tier,
		Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
	}).Run(ctx, args)
}
