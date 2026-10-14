// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestReadNamesTheToolchain(t *testing.T) {
	i := Read()
	if i.Version == "" || !strings.HasPrefix(i.Go, runtime.Version()+" ") {
		t.Errorf("Read() = %+v; want a version and the running toolchain", i)
	}
}

func TestString(t *testing.T) {
	for _, tt := range []struct {
		in   Info
		want string
	}{
		{Info{Version: "v0.4.0", Revision: "f6cb3679d75a4d903dbb0afcef8835e3b8823d78", Go: "go1.27.1 darwin/arm64"}, "alidade v0.4.0 (f6cb367, go1.27.1 darwin/arm64)"},
		{Info{Version: "v0.4.1-0.20260913052412-5518c313a159+dirty", Revision: "5518c313a159", Modified: true, Go: "go1.27.1 linux/amd64"}, "alidade v0.4.1-0.20260913052412-5518c313a159+dirty (5518c31, go1.27.1 linux/amd64)"},
		{Info{Version: "v0.4.0", Go: "go1.27.1 darwin/arm64"}, "alidade v0.4.0 (go1.27.1 darwin/arm64)"},
		{Info{Version: "devel", Go: "go1.27.1 darwin/arm64"}, "alidade devel (go1.27.1 darwin/arm64)"},
		{Info{Version: "v0.12.0", Go: "go1.27.1 linux/amd64", Embeds: []string{"v8", "cpython 3.14.8"}}, "alidade v0.12.0 (go1.27.1 linux/amd64; v8, cpython 3.14.8)"},
	} {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}
