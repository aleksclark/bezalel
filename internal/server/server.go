// Package server implements the MCP JSON-RPC server over Streamable HTTP.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aleksclark/bezalel/internal/lsp"
	"github.com/aleksclark/bezalel/internal/tools"
)

const (
	mcpProtocolVersion = "2024-11-05"
	serverName         = "bezalel"
	serverVersion      = "0.1.0"
)

// Options configures a Server.
type Options struct {
	// WorkingDir is the default working directory for tool execution.
	WorkingDir string
	// AuthToken, when non-empty, requires clients to present it as a
	// "Authorization: Bearer <token>" header on /mcp requests.
	AuthToken string
	// LSPServers configures the language servers bezalel will manage.
	LSPServers []lsp.ServerConfig
}

// Server is the MCP HTTP server.
type Server struct {
	toolbox   *tools.Toolbox
	mux       *http.ServeMux
	authToken string
}

// New creates a new MCP server with the given working directory and no auth.
func New(workingDir string) *Server {
	return NewWithOptions(Options{WorkingDir: workingDir})
}

// NewWithOptions creates a new MCP server from the given options.
func NewWithOptions(opts Options) *Server {
	s := &Server{
		toolbox: tools.NewToolboxWithOptions(tools.Options{
			WorkingDir: opts.WorkingDir,
			LSPServers: opts.LSPServers,
		}),
		mux:       http.NewServeMux(),
		authToken: opts.AuthToken,
	}
	s.mux.HandleFunc("POST /mcp", s.withAuth(s.handleMCP))
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
}

// AuthEnabled reports whether token authentication is configured.
func (s *Server) AuthEnabled() bool {
	return s.authToken != ""
}

// withAuth wraps a handler with bearer-token authentication. When no token is
// configured the request passes through unchecked.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authToken != "" && !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(jsonrpcResponse{
				JSONRPC: "2.0",
				Error:   &jsonrpcError{Code: -32001, Message: "Unauthorized"},
			})
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) == 1
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Shutdown cleans up server resources.
func (s *Server) Shutdown() {
	s.toolbox.Shutdown()
}

// JSON-RPC types
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonrpcError `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, -32700, "Parse error")
		return
	}

	if req.JSONRPC != "2.0" {
		writeError(w, req.ID, -32600, "Invalid Request: jsonrpc must be 2.0")
		return
	}

	slog.Debug("mcp request", "method", req.Method, "id", req.ID)

	var result any
	var rpcErr *jsonrpcError

	switch req.Method {
	case "initialize":
		result = s.handleInitialize(req.Params)
	case "tools/list":
		result = s.handleToolsList()
	case "tools/call":
		result, rpcErr = s.handleToolsCall(r.Context(), req.Params)
	case "notifications/initialized":
		// Client acknowledgment — no response needed for notifications
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		rpcErr = &jsonrpcError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	if rpcErr != nil {
		resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleInitialize(params json.RawMessage) any {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
	}
}

func (s *Server) handleToolsList() any {
	return map[string]any{
		"tools": []map[string]any{
			{
				"name":        "bash",
				"description": "Execute a shell command. Commands taking longer than 1 minute are automatically moved to background.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The command to execute",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Brief description of what the command does",
						},
						"working_dir": map[string]any{
							"type":        "string",
							"description": "Working directory (defaults to server working directory)",
						},
						"run_in_background": map[string]any{
							"type":        "boolean",
							"description": "Run immediately as a background job",
						},
					},
					"required": []string{"command"},
				},
			},
			{
				"name":        "job_output",
				"description": "Get the current output of a background job.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"description": "The ID of the background job",
						},
					},
					"required": []string{"job_id"},
				},
			},
			{
				"name":        "job_kill",
				"description": "Terminate a background job.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"description": "The ID of the background job to terminate",
						},
					},
					"required": []string{"job_id"},
				},
			},
			{
				"name":        "view",
				"description": "Read file contents with line-based pagination. Returns numbered lines. Max file size 5MB.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file to read",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "0-based line number to start reading from (default: 0)",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum number of lines to read (default: 2000)",
						},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "write",
				"description": "Create or overwrite a file. Creates parent directories automatically.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file to write",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Content to write to the file",
						},
					},
					"required": []string{"file_path", "content"},
				},
			},
			{
				"name":        "edit",
				"description": "Find-and-replace in a file. old_string must be unique unless replace_all is true. Returns a unified diff.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file to edit",
						},
						"old_string": map[string]any{
							"type":        "string",
							"description": "Text to find in the file (must be unique unless replace_all=true)",
						},
						"new_string": map[string]any{
							"type":        "string",
							"description": "Replacement text",
						},
						"replace_all": map[string]any{
							"type":        "boolean",
							"description": "Replace all occurrences instead of requiring uniqueness (default: false)",
						},
					},
					"required": []string{"file_path", "old_string", "new_string"},
				},
			},
			{
				"name":        "delete",
				"description": "Remove a file or directory. Non-empty directories require recursive=true.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file or directory to delete",
						},
						"recursive": map[string]any{
							"type":        "boolean",
							"description": "Recursively delete non-empty directories (default: false)",
						},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "ls",
				"description": "List directory contents in a tree-style format. Respects .gitignore-like patterns. Max 1000 entries.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Directory to list (defaults to working directory)",
						},
						"ignore": map[string]any{
							"type":        "array",
							"description": "Glob patterns to ignore",
							"items":       map[string]any{"type": "string"},
						},
						"depth": map[string]any{
							"type":        "integer",
							"description": "Maximum directory depth to traverse (default: 3)",
						},
					},
				},
			},
			{
				"name":        "glob",
				"description": "Find files matching a glob pattern. Uses ripgrep if available. Max 100 results.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Glob pattern to match files (e.g., '*.go', '**/*.json')",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Directory to search in (defaults to working directory)",
						},
					},
					"required": []string{"pattern"},
				},
			},
			{
				"name":        "grep",
				"description": "Search file contents by regex pattern. Uses ripgrep if available. Max 50 matches. Returns filepath:line_number:content format.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Regex pattern to search for",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Directory or file to search in (defaults to working directory)",
						},
						"include": map[string]any{
							"type":        "string",
							"description": "Glob filter to restrict which files are searched (e.g., '*.go')",
						},
					},
					"required": []string{"pattern"},
				},
			},
			{
				"name":        "multiedit",
				"description": "Apply multiple find-and-replace edits to a single file sequentially in one operation. All edits are applied atomically — if any edit fails, no changes are written. Returns a unified diff of the combined changes.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file to edit",
						},
						"edits": map[string]any{
							"type":        "array",
							"description": "Edits applied in order. Each must have old_string and new_string; set replace_all for multiple occurrences.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"old_string": map[string]any{
										"type":        "string",
										"description": "Text to find (must be unique unless replace_all=true)",
									},
									"new_string": map[string]any{
										"type":        "string",
										"description": "Replacement text",
									},
									"replace_all": map[string]any{
										"type":        "boolean",
										"description": "Replace all occurrences instead of requiring uniqueness (default: false)",
									},
								},
								"required": []string{"old_string", "new_string"},
							},
						},
					},
					"required": []string{"file_path", "edits"},
				},
			},
			{
				"name":        "download",
				"description": "Download a URL to a local file. Streams the response to disk, creating parent directories automatically. Returns the byte count on success.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "HTTP(S) URL to download",
						},
						"file_path": map[string]any{
							"type":        "string",
							"description": "Local path to save the downloaded content",
						},
						"timeout": map[string]any{
							"type":        "integer",
							"description": "Timeout in seconds (default 120, max 600)",
						},
					},
					"required": []string{"url", "file_path"},
				},
			},
			{
				"name":        "fetch",
				"description": "Fetch a URL and return its content inline. HTML is converted to the requested format. Large responses are truncated.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "HTTP(S) URL to fetch",
						},
						"format": map[string]any{
							"type":        "string",
							"description": "Output format: text, markdown (default), or html",
							"enum":        []string{"text", "markdown", "html"},
						},
						"timeout": map[string]any{
							"type":        "integer",
							"description": "Timeout in seconds (default 120, max 600)",
						},
					},
					"required": []string{"url"},
				},
			},
			{
				"name":        "web_fetch",
				"description": "Fetch a URL and return its content. Behaves like fetch but spills oversized content to a temp file in the working directory and returns the path.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "HTTP(S) URL to fetch",
						},
						"format": map[string]any{
							"type":        "string",
							"description": "Output format: text, markdown (default), or html",
							"enum":        []string{"text", "markdown", "html"},
						},
						"timeout": map[string]any{
							"type":        "integer",
							"description": "Timeout in seconds (default 120, max 600)",
						},
					},
					"required": []string{"url"},
				},
			},
			{
				"name":        "lsp_diagnostics",
				"description": "Get compiler/linter diagnostics from configured language servers. Provide file_path for a single file, or omit it for project-wide diagnostics.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "File to diagnose (omit for project-wide diagnostics)",
						},
					},
				},
			},
			{
				"name":        "lsp_references",
				"description": "Find all references to a symbol via a language server. Uses grep to locate candidate positions, then resolves them with textDocument/references.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"symbol": map[string]any{
							"type":        "string",
							"description": "The symbol name to find references for",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Directory or file to scope the search (defaults to working directory)",
						},
					},
					"required": []string{"symbol"},
				},
			},
			{
				"name":        "lsp_restart",
				"description": "Restart a language server (or all of them when name is omitted). Servers reinitialize lazily on next use.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "Name of the language server to restart (omit to restart all)",
						},
					},
				},
			},
		},
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *jsonrpcError) {
	var call toolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "Invalid params"}
	}

	switch call.Name {
	case "bash":
		var p tools.BashParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		result, err := s.toolbox.ExecBash(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(result.Output, false), nil

	case "job_output":
		var p tools.JobOutputParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.GetJobOutput(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "job_kill":
		var p tools.JobKillParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.KillJob(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "view":
		var p tools.ViewParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.View(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "write":
		var p tools.WriteParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Write(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "edit":
		var p tools.EditParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Edit(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "delete":
		var p tools.DeleteParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Delete(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "ls":
		var p tools.LsParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Ls(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "glob":
		var p tools.GlobParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Glob(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "grep":
		var p tools.GrepParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Grep(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "multiedit":
		var p tools.MultiEditParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.MultiEdit(p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "download":
		var p tools.DownloadParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Download(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "fetch":
		var p tools.FetchParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.Fetch(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "web_fetch":
		var p tools.WebFetchParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.WebFetch(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "lsp_diagnostics":
		var p tools.LspDiagnosticsParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.LspDiagnostics(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "lsp_references":
		var p tools.LspReferencesParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.LspReferences(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	case "lsp_restart":
		var p tools.LspRestartParams
		if err := json.Unmarshal(call.Arguments, &p); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("Invalid arguments: %s", err)}
		}
		output, err := s.toolbox.LspRestart(ctx, p)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(output, false), nil

	default:
		return toolResult(fmt.Sprintf("Unknown tool: %s", call.Name), true), nil
	}
}

func toolResult(text string, isError bool) map[string]any {
	content := []map[string]any{
		{"type": "text", "text": text},
	}
	return map[string]any{
		"content": content,
		"isError": isError,
	}
}

func writeError(w http.ResponseWriter, id any, code int, message string) {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: message},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
