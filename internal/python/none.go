// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build !python

package python

import "context"

// Available reports whether this build can read Python.
func Available() bool { return false }

// CPythonVersion is empty: no CPython is linked.
const CPythonVersion = ""

// Engine is empty: there is no engine, and no answer to keep.
func Engine() string { return "" }

// Analyze is unavailable without the python tag.
func Analyze(root string, files []File, workers int) (*Result, error) { return nil, ErrUnavailable }

// AnalyzeEach is unavailable without the python tag.
func AnalyzeEach(ctx context.Context, root string, files []File, workers int, each func(rel string, r *Result)) (*Result, error) {
	return nil, ErrUnavailable
}

// Names is unavailable without the python tag.
func Names(root string, files, others []File) (map[string]*Result, error) { return nil, ErrUnavailable }
