// Package server implements the MCP JSON-RPC server over Streamable HTTP.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/aleksclark/bezalel/internal/tools"
)

const (
	mcpProtocolVersion = "2024-11-05"
	serverName         = "bezalel"
	serverVersion      = "0.1.0"
)

// Server is the MCP HTTP server.
type Server struct {
	toolbox *tools.Toolbox
	mux     *http.ServeMux
	mu      sync.RWMutex
}

// New creates a new MCP server.
func New(workingDir string) *Server {
	s := &Server{
		toolbox: tools.NewToolbox(workingDir),
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /mcp", s.handleMCP)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
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
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *jsonrpcError `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	json.NewEncoder(w).Encode(resp)
}
