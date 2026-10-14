// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build v8

package svelte

import (
	"encoding/json"
	"fmt"
	"sync"

	v8 "github.com/tommie/v8go"
)

// Available reports whether this build translates components.
func Available() bool { return true }

// engine is the one V8 isolate the process translates with, made on the
// first component; an isolate runs one call at a time.
var engine struct {
	sync.Mutex
	once sync.Once
	iso  *v8.Isolate
	fn   *v8.Function
	err  error
}

// prelude hides Node's globals from the bundle, so TypeScript's own code
// inside it takes no Node path.
const prelude = "var process = undefined, require = undefined, module = undefined;\n"

func start() {
	src, err := Bundle()
	if err != nil {
		engine.err = err
		return
	}
	engine.iso = v8.NewIsolate()
	ctx := v8.NewContext(engine.iso)
	if _, err := ctx.RunScript(prelude+string(src), "svelte2tsx.js"); err != nil {
		engine.err = fmt.Errorf("svelte: load svelte2tsx: %w", err)
		return
	}
	v, err := ctx.Global().Get("convert")
	if err == nil {
		engine.fn, err = v.AsFunction()
	}
	engine.err = err
}

func convert(src, filename string) (string, string, error) {
	engine.once.Do(start)
	if engine.err != nil {
		return "", "", engine.err
	}
	engine.Lock()
	defer engine.Unlock()
	s, err := v8.NewValue(engine.iso, src)
	if err != nil {
		return "", "", err
	}
	f, err := v8.NewValue(engine.iso, filename)
	if err != nil {
		return "", "", err
	}
	res, err := engine.fn.Call(v8.Undefined(engine.iso), s, f)
	if err != nil {
		return "", "", fmt.Errorf("svelte2tsx: %w", err)
	}
	var out struct {
		Code     string `json:"code"`
		Mappings string `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(res.String()), &out); err != nil {
		return "", "", err
	}
	return out.Code, out.Mappings, nil
}
