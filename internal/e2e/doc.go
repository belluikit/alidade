// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package e2e checks what an agent receives from alidade: the binary
// built as a release is, serving a real repository over stdio, read the
// way an MCP client reads it (#174). The oracle harness measures the
// graph; this measures the surface between the graph and the agent,
// where #160 shipped — every parameter described as exclude_tests — with
// no check to catch it.
//
// TestE2E runs only when ALIDADE_E2E names repository roots
// (scripts/mcp-e2e.sh). ALIDADE_E2E_BIN runs an existing binary instead
// of building one, so a release can be checked as shipped, and
// ALIDADE_E2E_REF reads the descriptions from the source it was built
// from: ALIDADE_E2E_BIN=$(which alidade) ALIDADE_E2E_REF=v0.7.0 fails on
// #160.
package e2e
