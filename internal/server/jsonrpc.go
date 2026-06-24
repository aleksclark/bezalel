package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// JSON-RPC 2.0 message types.

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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleMCP decodes a JSON-RPC request, routes it to an MCP method handler, and
// writes the JSON-RPC response.
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
		result = s.registry.list()
	case "tools/call":
		result, rpcErr = s.handleToolsCall(r.Context(), req.Params)
	case "notifications/initialized":
		// Client acknowledgment — no response needed for notifications.
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		rpcErr = &jsonrpcError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	writeResponse(w, req.ID, result, rpcErr)
}

func writeResponse(w http.ResponseWriter, id, result any, rpcErr *jsonrpcError) {
	resp := jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
