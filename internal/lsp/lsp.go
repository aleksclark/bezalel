// Package lsp implements a minimal Language Server Protocol client and a
// lifecycle manager for language-server subprocesses. Language servers are
// assumed to be installed in the pod environment; bezalel spawns, initializes,
// restarts, and shuts them down on demand.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aleksclark/bezalel/internal/version"
)

// ServerConfig describes a single language server and the files it handles.
type ServerConfig struct {
	// Name is a unique identifier for the server (e.g. "gopls").
	Name string `mapstructure:"name"`
	// Command is the executable to run.
	Command string `mapstructure:"command"`
	// Args are extra arguments passed to the command.
	Args []string `mapstructure:"args"`
	// Env are additional environment variables (KEY=VALUE) for the process.
	Env []string `mapstructure:"env"`
	// Extensions are the file extensions (with leading dot) this server handles.
	Extensions []string `mapstructure:"extensions"`
	// RootMarkers are filenames that, when found in an ancestor directory,
	// mark the workspace root for a given file (e.g. "go.mod").
	RootMarkers []string `mapstructure:"root_markers"`
	// LanguageID is the LSP languageId for opened documents (e.g. "go").
	LanguageID string `mapstructure:"language_id"`
}

// Position is a zero-based line/character location in a document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span within a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range within a specific document URI.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Diagnostic is a single problem reported by a language server.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     any    `json:"code"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// SeverityString returns a human-readable severity label.
func (d Diagnostic) SeverityString() string {
	switch d.Severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return "unknown"
	}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type diagState struct {
	received bool
	diags    []Diagnostic
}

// Client is a single language-server subprocess speaking LSP over stdio.
type Client struct {
	cfg     ServerConfig
	rootDir string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *strings.Builder

	writeMu sync.Mutex

	pendMu  sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMessage

	diagMu sync.Mutex
	diags  map[string]*diagState

	openMu sync.Mutex
	opened map[string]int // uri -> version

	closed    chan struct{}
	closeOnce sync.Once
}

// startClient launches and initializes a language server.
func startClient(ctx context.Context, cfg ServerConfig, rootDir string) (*Client, error) {
	// #nosec G204 -- the command is operator-configured, not user-supplied.
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = rootDir
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", cfg.Command, err)
	}

	c := &Client{
		cfg:     cfg,
		rootDir: rootDir,
		cmd:     cmd,
		stdin:   stdin,
		stderr:  &stderrBuf,
		pending: make(map[int64]chan rpcMessage),
		diags:   make(map[string]*diagState),
		opened:  make(map[string]int),
		closed:  make(chan struct{}),
	}

	go c.readLoop(bufio.NewReader(stdout))

	if err := c.initialize(ctx); err != nil {
		_ = c.Shutdown()
		return nil, fmt.Errorf("initialize %q: %w", cfg.Name, err)
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   pathToURI(c.rootDir),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true, "dynamicRegistration": false},
				"publishDiagnostics": map[string]any{},
				"references":         map[string]any{"dynamicRegistration": false},
			},
			"workspace": map[string]any{
				"configuration": true,
			},
		},
		"clientInfo": map[string]any{"name": version.Name, "version": version.Number},
		"workspaceFolders": []map[string]any{
			{"uri": pathToURI(c.rootDir), "name": filepath.Base(c.rootDir)},
		},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *Client) readLoop(r *bufio.Reader) {
	for {
		msg, err := readMessage(r)
		if err != nil {
			c.failPending(err)
			c.closeOnce.Do(func() { close(c.closed) })
			return
		}
		c.dispatch(msg)
	}
}

func (c *Client) dispatch(msg rpcMessage) {
	if msg.Method != "" {
		if len(msg.ID) > 0 {
			c.respondToServerRequest(msg)
			return
		}
		if msg.Method == "textDocument/publishDiagnostics" {
			c.handlePublishDiagnostics(msg.Params)
		}
		return
	}
	// Response to one of our requests.
	id, ok := parseIntID(msg.ID)
	if !ok {
		return
	}
	c.pendMu.Lock()
	ch := c.pending[id]
	c.pendMu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

// respondToServerRequest answers server-to-client requests so real servers
// (e.g. gopls) don't block waiting on us.
func (c *Client) respondToServerRequest(msg rpcMessage) {
	var result any
	switch msg.Method {
	case "workspace/configuration":
		// Return one null config entry per requested item.
		var p struct {
			Items []any `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		arr := make([]any, len(p.Items))
		result = arr
	default:
		result = nil
	}
	resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	if data, err := json.Marshal(result); err == nil {
		resp.Result = data
	}
	_ = c.writeMessage(resp)
}

func (c *Client) handlePublishDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	c.diagMu.Lock()
	c.diags[p.URI] = &diagState{received: true, diags: p.Diagnostics}
	c.diagMu.Unlock()
}

func (c *Client) failPending(err error) {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	for id, ch := range c.pending {
		select {
		case ch <- rpcMessage{Error: &rpcError{Code: -1, Message: err.Error()}}:
		default:
		}
		delete(c.pending, id)
	}
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMessage, 1)

	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	idJSON, _ := json.Marshal(id)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	if err := c.writeMessage(rpcMessage{JSONRPC: "2.0", ID: idJSON, Method: method, Params: paramsJSON}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("language server %q exited: %s", c.cfg.Name, strings.TrimSpace(c.stderr.String()))
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("lsp error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	return c.writeMessage(rpcMessage{JSONRPC: "2.0", Method: method, Params: paramsJSON})
}

func (c *Client) writeMessage(m rpcMessage) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// DidOpen sends a textDocument/didOpen notification for the file if it has not
// already been opened, making the server aware of its content.
func (c *Client) DidOpen(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	uri := pathToURI(abs)

	c.openMu.Lock()
	_, already := c.opened[uri]
	c.openMu.Unlock()
	if already {
		return nil
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	langID := c.cfg.LanguageID
	if langID == "" {
		langID = "plaintext"
	}

	c.openMu.Lock()
	c.opened[uri] = 1
	c.openMu.Unlock()

	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": langID,
			"version":    1,
			"text":       string(data),
		},
	})
}

// Diagnostics opens the file and waits up to the given timeout for the server
// to publish diagnostics for it. A nil slice with received=false means no
// diagnostics were published within the timeout.
func (c *Client) Diagnostics(ctx context.Context, path string, wait time.Duration) (diags []Diagnostic, received bool, err error) {
	if err := c.DidOpen(path); err != nil {
		return nil, false, err
	}
	abs, _ := filepath.Abs(path)
	uri := pathToURI(abs)

	deadline := time.Now().Add(wait)
	for {
		c.diagMu.Lock()
		st := c.diags[uri]
		c.diagMu.Unlock()
		if st != nil && st.received {
			return st.diags, true, nil
		}
		if time.Now().After(deadline) {
			return nil, false, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-c.closed:
			return nil, false, fmt.Errorf("language server %q exited", c.cfg.Name)
		case <-time.After(40 * time.Millisecond):
		}
	}
}

// AllDiagnostics returns every diagnostic the server has published so far,
// keyed by file URI.
func (c *Client) AllDiagnostics() map[string][]Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	out := make(map[string][]Diagnostic, len(c.diags))
	for uri, st := range c.diags {
		if st.received {
			out[uri] = st.diags
		}
	}
	return out
}

// References returns all references to the symbol at the given position.
func (c *Client) References(ctx context.Context, path string, pos Position) ([]Location, error) {
	if err := c.DidOpen(path); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(path)
	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(abs)},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": true},
	}
	res, err := c.call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 || string(res) == "null" {
		return nil, nil
	}
	var locs []Location
	if err := json.Unmarshal(res, &locs); err != nil {
		return nil, fmt.Errorf("decode references: %w", err)
	}
	return locs, nil
}

// Shutdown gracefully stops the language server, falling back to a kill.
func (c *Client) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	_ = c.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func readMessage(r *bufio.Reader) (rpcMessage, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return rpcMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, val, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				n, err := strconv.Atoi(strings.TrimSpace(val))
				if err != nil {
					return rpcMessage{}, fmt.Errorf("bad Content-Length: %w", err)
				}
				contentLength = n
			}
		}
	}
	if contentLength <= 0 {
		return rpcMessage{}, fmt.Errorf("missing or invalid Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return rpcMessage{}, err
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return rpcMessage{}, fmt.Errorf("decode message: %w", err)
	}
	return msg, nil
}

func parseIntID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	return 0, false
}

func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String()
}

// URIToPath converts a file:// URI to a filesystem path.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	if u.Scheme != "file" {
		return uri
	}
	return u.Path
}
