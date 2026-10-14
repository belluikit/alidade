// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build !v8

package svelte

// Available reports whether this build translates components.
func Available() bool { return false }

func convert(string, string) (string, string, error) { return "", "", ErrUnavailable }
