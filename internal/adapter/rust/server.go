// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

// server is rust-analyzer's server on one workspace, started the first
// time a step asks for it and shared by the steps after (generated code,
// then implementations): loading a workspace is most of a server's cost.
// nil when it cannot start, which is reported once, on the workspace.
func (x *extractor) server(ws string) *lsp {
	if l, asked := x.servers[ws]; asked {
		return l
	}
	l, err := startLSP(ws)
	if err != nil {
		x.skip(ws, 0, err)
		l = nil
	} else {
		x.report.ServerColumns = "utf-16"
		if l.utf8 {
			x.report.ServerColumns = "utf-8"
		}
	}
	if x.servers == nil {
		x.servers = map[string]*lsp{}
	}
	x.servers[ws] = l
	return l
}

// closeServers stops every server a step started.
func (x *extractor) closeServers() {
	for _, l := range x.servers {
		if l != nil {
			l.close()
		}
	}
	x.servers = nil
}
