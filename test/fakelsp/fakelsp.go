// Package fakelsp implements a minimal, deterministic language server used by
// bezalel's tests. It speaks just enough of the Language Server Protocol to
// exercise the lsp client/manager and the lsp_* tools without depending on a
// real language server being installed.
//
// Behavior:
//   - initialize -> advertises referencesProvider
//   - textDocument/didOpen -> publishes one error diagnostic if the document
//     text contains the marker "BUG", otherwise an empty diagnostic set
//   - textDocument/references -> returns two deterministic locations
//   - shutdown/exit -> terminates cleanly
package fakelsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// Run reads LSP messages from stdin and writes responses to stdout until an
// exit notification is received. It calls os.Exit on exit.
func Run() {
	r := bufio.NewReader(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for {
		msg, err := read(r)
		if err != nil {
			return
		}
		switch msg.Method {
		case "initialize":
			reply(w, msg.ID, map[string]any{
				"capabilities": map[string]any{
					"referencesProvider": true,
					"textDocumentSync":   1,
				},
				"serverInfo": map[string]any{"name": "fakelsp", "version": "0.1.0"},
			})
		case "initialized":
			// notification, no reply
		case "textDocument/didOpen":
			publishDiagnostics(w, msg.Params)
		case "textDocument/references":
			references(w, msg.ID, msg.Params)
		case "shutdown":
			replyRaw(w, msg.ID, json.RawMessage("null"))
		case "exit":
			_ = w.Flush()
			os.Exit(0)
		default:
			if len(msg.ID) > 0 {
				replyRaw(w, msg.ID, json.RawMessage("null"))
			}
		}
	}
}

func publishDiagnostics(w *bufio.Writer, params json.RawMessage) {
	var p struct {
		TextDocument struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	diags := []map[string]any{}
	for i, line := range strings.Split(p.TextDocument.Text, "\n") {
		col := strings.Index(line, "BUG")
		if col < 0 {
			continue
		}
		diags = append(diags, map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": i, "character": col},
				"end":   map[string]any{"line": i, "character": col + 3},
			},
			"severity": 1,
			"source":   "fakelsp",
			"message":  "found BUG marker",
		})
	}

	notify(w, "textDocument/publishDiagnostics", map[string]any{
		"uri":         p.TextDocument.URI,
		"diagnostics": diags,
	})
}

func references(w *bufio.Writer, id json.RawMessage, params json.RawMessage) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
	}
	_ = json.Unmarshal(params, &p)

	loc := func(line, char int) map[string]any {
		return map[string]any{
			"uri": p.TextDocument.URI,
			"range": map[string]any{
				"start": map[string]any{"line": line, "character": char},
				"end":   map[string]any{"line": line, "character": char + 1},
			},
		}
	}
	reply(w, id, []map[string]any{
		loc(p.Position.Line, p.Position.Character),
		loc(p.Position.Line+10, 0),
	})
}

func reply(w *bufio.Writer, id json.RawMessage, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	replyRaw(w, id, data)
}

func replyRaw(w *bufio.Writer, id json.RawMessage, result json.RawMessage) {
	write(w, message{JSONRPC: "2.0", ID: id, Result: result})
}

func notify(w *bufio.Writer, method string, params any) {
	data, err := json.Marshal(params)
	if err != nil {
		return
	}
	write(w, message{JSONRPC: "2.0", Method: method, Params: data})
}

func write(w *bufio.Writer, m message) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data))
	_, _ = w.Write(data)
	_ = w.Flush()
}

func read(r *bufio.Reader) (message, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, val, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				n, err := strconv.Atoi(strings.TrimSpace(val))
				if err != nil {
					return message{}, err
				}
				contentLength = n
			}
		}
	}
	if contentLength <= 0 {
		return message{}, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return message{}, err
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return message{}, err
	}
	return m, nil
}
