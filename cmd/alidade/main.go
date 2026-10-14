// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Command alidade serves a repository's graph over MCP, or writes it out.
// See internal/cli for the commands.
package main

import (
	"os"

	"github.com/trendvidia/alidade/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
