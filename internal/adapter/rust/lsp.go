// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package rust

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// An lsp is rust-analyzer's language server on one workspace, for the
// questions its SCIP index does not answer (ADR 009): the implementations
// of a trait, which a macro may write. It speaks JSON-RPC over the
// server's standard input and output, one request at a time.
type lsp struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	next int
	// unlock removes the server's copy of the lock file.
	unlock func()
	// utf8 says the server counts columns in UTF-8 bytes, as the index
	// does; otherwise in UTF-16 code units, LSP's default.
	utf8 bool
	// stderr is the server's log, at RA_LOG=error: where it says which
	// build scripts failed (#85). health and status are its last status
	// report's, "ok" and "" when nothing went wrong.
	stderr         *logBuffer
	health, status string
}

// lspReady bounds the wait for rust-analyzer to finish loading a
// workspace: Cargo's build runs first, as for the index.
var lspReady = 30 * time.Minute

// lspEncodings are the column encodings the client offers, UTF-8 first,
// which the index counts in. A variable for the tests, which make the
// server count in UTF-16, as one that cannot count in UTF-8 would.
var lspEncodings = []string{"utf-8", "utf-16"}

// startLSP starts rust-analyzer's server on the workspace at ws and waits
// until it says it is quiescent, done loading and indexing: before then a
// question can be answered from a partial workspace, and a trait with no
// implementations answers empty either way, so asking again until an
// answer comes cannot tell the two apart.
func startLSP(ws string) (*lsp, error) {
	cmd := exec.Command("rust-analyzer")
	cmd.Dir = ws
	unlock, err := lockOutside(cmd, ws)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Environ(), "RA_LOG=error") // a build script's failure is logged at error, whatever the user's level
	stderr := &logBuffer{}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		unlock()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		unlock()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		unlock()
		return nil, fmt.Errorf("rust-analyzer: %w", err)
	}
	l := &lsp{cmd: cmd, in: in, out: bufio.NewReader(out), unlock: unlock, stderr: stderr}
	init := map[string]any{
		"processId": nil,
		"rootUri":   fileURI(ws),
		"capabilities": map[string]any{
			"general":      map[string]any{"positionEncodings": lspEncodings},
			"experimental": map[string]any{"serverStatusNotification": true},
			"textDocument": map[string]any{"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true}},
		},
	}
	var res struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if _, err := l.call("initialize", init, &res); err != nil {
		l.close()
		return nil, err
	}
	l.utf8 = res.Capabilities.PositionEncoding == "utf-8"
	if err := l.notify("initialized", map[string]any{}); err != nil {
		l.close()
		return nil, err
	}
	if err := l.waitQuiescent(); err != nil {
		l.close()
		return nil, err
	}
	return l, nil
}

// A message is one JSON-RPC message from the server.
type message struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result json.RawMessage  `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (l *lsp) waitQuiescent() error {
	deadline := time.AfterFunc(lspReady, func() { _ = l.cmd.Process.Kill() })
	defer deadline.Stop()
	for {
		m, err := l.read()
		if err != nil {
			return fmt.Errorf("rust-analyzer did not finish loading the workspace: %w", err)
		}
		if err := l.answer(m); err != nil {
			return err
		}
		if m.Method != "experimental/serverStatus" {
			continue
		}
		var st struct {
			Quiescent bool    `json:"quiescent"`
			Health    string  `json:"health"`
			Message   *string `json:"message"`
		}
		if json.Unmarshal(m.Params, &st) != nil {
			continue
		}
		l.health, l.status = st.Health, ""
		if st.Message != nil {
			l.status = *st.Message
		}
		if st.Quiescent {
			return nil
		}
	}
}

// call sends a request and decodes its result into v, answering what the
// server asks meanwhile.
func (l *lsp) call(method string, params, v any) (json.RawMessage, error) {
	l.next++
	id := l.next
	if err := l.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		m, err := l.read()
		if err != nil {
			return nil, fmt.Errorf("rust-analyzer: %s: %w", method, err)
		}
		if m.ID == nil || m.Method != "" || string(*m.ID) != strconv.Itoa(id) {
			if err := l.answer(m); err != nil {
				return nil, err
			}
			continue
		}
		if m.Error != nil {
			return nil, fmt.Errorf("rust-analyzer: %s: %s", method, m.Error.Message)
		}
		if v != nil {
			if err := json.Unmarshal(m.Result, v); err != nil {
				return nil, fmt.Errorf("rust-analyzer: %s: %w", method, err)
			}
		}
		return m.Result, nil
	}
}

// answer replies to a request from the server, which a client must: with
// nothing, since the layer offers no client feature.
func (l *lsp) answer(m *message) error {
	if m.ID == nil || m.Method == "" {
		return nil
	}
	return l.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": nil})
}

func (l *lsp) notify(method string, params any) error {
	return l.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (l *lsp) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(l.in, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = l.in.Write(b)
	return err
}

func (l *lsp) read() (*message, error) {
	length := -1
	for {
		line, err := l.out.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, "Content-Length") {
			if length, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, errors.New("a message without a Content-Length")
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(l.out, b); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// close ends the server: politely, then for good.
func (l *lsp) close() {
	defer l.unlock()
	if l.cmd.ProcessState != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = l.call("shutdown", nil, nil)
		_ = l.notify("exit", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	_ = l.in.Close()
	_ = l.cmd.Process.Kill()
	_ = l.cmd.Wait()
}

// A location is where an answer points: a file and a position, both
// zero-based, its column counted as the server negotiated ([lsp.utf8]).
type location struct {
	URI   string `json:"uri"`
	Range struct {
		Start struct {
			Line      int32 `json:"line"`
			Character int32 `json:"character"`
		} `json:"start"`
	} `json:"range"`
	TargetURI   string `json:"targetUri"`
	TargetRange *struct {
		Start struct {
			Line      int32 `json:"line"`
			Character int32 `json:"character"`
		} `json:"start"`
	} `json:"targetSelectionRange"`
}

// at is a location's file and start, whichever form the server used.
func (lo location) at() (path string, p pos) {
	uri := lo.URI
	start := lo.Range.Start
	if lo.TargetURI != "" {
		uri = lo.TargetURI
		if lo.TargetRange != nil {
			start = lo.TargetRange.Start
		}
	}
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		path = u.Path
	}
	return path, pos{start.Line, start.Character}
}

// implementations asks where the trait whose name starts at p in dc is
// implemented; definition where the item named at p is defined. Each
// answer's columns are bytes, as the index's are, whatever the server
// counts in.
func (l *lsp) implementations(dc *doc, p pos, text func(path string) []byte) ([]location, error) {
	return l.locations("textDocument/implementation", dc, p, text)
}

func (l *lsp) definition(dc *doc, p pos, text func(path string) []byte) ([]location, error) {
	return l.locations("textDocument/definition", dc, p, text)
}

func (l *lsp) locations(method string, dc *doc, p pos, text func(path string) []byte) ([]location, error) {
	col := p.col
	if !l.utf8 {
		col = utf16Col(lineOf(dc.text, p.line), p.col)
	}
	var locs []location
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(dc.abs)},
		"position":     map[string]any{"line": p.line, "character": col},
	}
	if _, err := l.call(method, params, &locs); err != nil {
		return nil, err
	}
	if !l.utf8 {
		for i := range locs {
			path, at := locs[i].at()
			locs[i].setCol(byteCol(lineOf(text(path), at.line), at.col))
		}
	}
	return locs, nil
}

// A symbol is one item of a file as the server lists it: its name, its
// LSP SymbolKind, its lines, and the items inside it.
type symbol struct {
	Name string `json:"name"`
	Kind int    `json:"kind"`
	// Range is the whole item, its attributes included; SelectionRange
	// its name.
	Range          lineRange `json:"range"`
	SelectionRange lineRange `json:"selectionRange"`
	Children       []symbol  `json:"children"`
}

// A lineRange is where an LSP range starts, its column counted as the
// server negotiated, and the line it ends on.
type lineRange struct {
	Start struct {
		Line      int32 `json:"line"`
		Character int32 `json:"character"`
	} `json:"start"`
	End struct {
		Line int32 `json:"line"`
	} `json:"end"`
}

// symbols lists the items of a file, nested, as the server's outline
// does (a module's items under it, a struct's fields, an impl's methods).
func (l *lsp) symbols(path string) ([]symbol, error) {
	var out []symbol
	_, err := l.call("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": fileURI(path)}}, &out)
	return out, err
}

// setCol replaces a location's start column, in whichever form it came.
func (lo *location) setCol(c int32) {
	if lo.TargetURI != "" && lo.TargetRange != nil {
		lo.TargetRange.Start.Character = c
		return
	}
	lo.Range.Start.Character = c
}

func lineOf(text []byte, n int32) []byte {
	for i := int32(0); i < n; i++ {
		j := bytes.IndexByte(text, '\n')
		if j < 0 {
			return nil
		}
		text = text[j+1:]
	}
	if j := bytes.IndexByte(text, '\n'); j >= 0 {
		text = text[:j]
	}
	return text
}

// utf16Col is the UTF-16 column of the byte column b on line; byteCol
// the reverse.
func utf16Col(line []byte, b int32) int32 {
	var n int32
	for i, r := range string(line) {
		if int32(i) >= b {
			break
		}
		n += int32(len(utf16.Encode([]rune{r})))
	}
	return n
}

func byteCol(line []byte, u int32) int32 {
	var n int32
	for i, r := range string(line) {
		if n >= u {
			return int32(i)
		}
		n += int32(len(utf16.Encode([]rune{r})))
	}
	return int32(len(line))
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// An outgoingCall is a call the server's call hierarchy finds in a
// function: the file and name position of the function called, and each
// range in the caller that names it. Columns are bytes.
type outgoingCall struct {
	path string
	to   pos
	from [][2]pos
}

// lspRange is an LSP range, both ends.
type lspRange struct {
	Start struct {
		Line      int32 `json:"line"`
		Character int32 `json:"character"`
	} `json:"start"`
	End struct {
		Line      int32 `json:"line"`
		Character int32 `json:"character"`
	} `json:"end"`
}

// outgoing asks the server's call hierarchy what the function whose name
// starts at p in dc calls: what rust-analyzer answers for a function of a
// file that an include! reads, which its index has no document of (#88).
func (l *lsp) outgoing(dc *doc, p pos, text func(path string) []byte) ([]outgoingCall, error) {
	col := p.col
	if !l.utf8 {
		col = utf16Col(lineOf(dc.text, p.line), p.col)
	}
	var items []json.RawMessage
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(dc.abs)},
		"position":     map[string]any{"line": p.line, "character": col},
	}
	if _, err := l.call("textDocument/prepareCallHierarchy", params, &items); err != nil || len(items) == 0 {
		return nil, err
	}
	var calls []struct {
		To struct {
			URI            string   `json:"uri"`
			SelectionRange lspRange `json:"selectionRange"`
		} `json:"to"`
		FromRanges []lspRange `json:"fromRanges"`
	}
	if _, err := l.call("callHierarchy/outgoingCalls", map[string]any{"item": items[0]}, &calls); err != nil {
		return nil, err
	}
	var out []outgoingCall
	for _, c := range calls {
		path := location{URI: c.To.URI}
		target, _ := path.at()
		oc := outgoingCall{path: target, to: l.bytePos(text(target), c.To.SelectionRange.Start.Line, c.To.SelectionRange.Start.Character)}
		for _, r := range c.FromRanges {
			oc.from = append(oc.from, [2]pos{l.bytePos(dc.text, r.Start.Line, r.Start.Character), l.bytePos(dc.text, r.End.Line, r.End.Character)})
		}
		out = append(out, oc)
	}
	return out, nil
}

// bytePos is a server position as the index counts it, in bytes.
func (l *lsp) bytePos(text []byte, line, col int32) pos {
	if !l.utf8 {
		col = byteCol(lineOf(text, line), col)
	}
	return pos{line, col}
}
