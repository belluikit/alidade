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
	l := &lsp{cmd: cmd, in: in, out: bufio.NewReader(out), unlock: unlock}
	init := map[string]any{
		"processId": nil,
		"rootUri":   fileURI(ws),
		"capabilities": map[string]any{
			"general":      map[string]any{"positionEncodings": lspEncodings},
			"experimental": map[string]any{"serverStatusNotification": true},
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
			Quiescent bool `json:"quiescent"`
		}
		if json.Unmarshal(m.Params, &st) == nil && st.Quiescent {
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

// implementations asks where the trait named at p in dc is implemented. p
// and the answers' positions are the index's, UTF-8 bytes; a server that
// counts in UTF-16 has them converted, by the text of each file.
func (l *lsp) implementations(dc *doc, p pos, text func(path string) []byte) ([]location, error) {
	col := p.col
	if !l.utf8 {
		col = utf16Col(lineOf(dc.text, p.line), p.col)
	}
	var locs []location
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(dc.abs)},
		"position":     map[string]any{"line": p.line, "character": col},
	}
	if _, err := l.call("textDocument/implementation", params, &locs); err != nil {
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
