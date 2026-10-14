// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package graph

import (
	"errors"
	"io/fs"
)

// A Skip is an input a layer read and could not represent: a file whose
// path no ID can carry, a link or reference whose target cannot be named,
// a directory the walk could not open. The layer leaves that one input out
// and keeps going, and the Skip is how the build says so (#92) — one bad
// name must cost one fact, never the whole graph, and never silently.
//
// File is repo-relative where it can be made so, else as the layer saw
// it; Line is 0 when the input is a whole file. Layer is filled in by the
// build, which knows which adapter reported it.
type Skip struct {
	Layer, File string
	Line        int
	Reason      string
}

// Skip records that file, at line (0 for the whole file), was left out
// for err. The path is made repo-relative when it can be, and the reason
// is rendered by [SkipReason].
func (m *Minter) Skip(file string, line int, err error) Skip {
	if rel, perr := m.PosPath(file); perr == nil {
		file = string(rel)
	}
	return Skip{File: file, Line: line, Reason: SkipReason(err)}
}

// SkipReason renders err as a Skip's reason. A file system error gives
// its operation and cause without the absolute path it wraps, which File
// already says and which would differ by machine — a reason reaches the
// build's output, where a machine-specific path makes two builds of one
// tree disagree. Minting a Skip without a Minter (a walk that has no
// Builder yet) goes through here.
func SkipReason(err error) string {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}
