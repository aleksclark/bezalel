package server

import (
	"context"
	"encoding/json"

	"github.com/aleksclark/bezalel/internal/version"
)

// mcpProtocolVersion is the MCP protocol revision bezalel implements.
const mcpProtocolVersion = "2024-11-05"

// handleInitialize answers the MCP initialize handshake. It echoes the client's
// requested protocol version when provided, falling back to the version bezalel
// implements.
func (s *Server) handleInitialize(params json.RawMessage) any {
	protocol := mcpProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
			protocol = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": protocol,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    version.Name,
			"version": version.Number,
		},
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall dispatches a tools/call request through the registry.
func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *jsonrpcError) {
	var call toolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "Invalid params"}
	}
	return s.registry.call(ctx, call.Name, call.Arguments)
}
