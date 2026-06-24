// Package server implements the MCP JSON-RPC server over Streamable HTTP.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aleksclark/bezalel/internal/lsp"
	"github.com/aleksclark/bezalel/internal/tools"
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
	registry  *registry
	mux       *http.ServeMux
	authToken string
}

// New creates a new MCP server with the given working directory and no auth.
func New(workingDir string) *Server {
	return NewWithOptions(Options{WorkingDir: workingDir})
}

// NewWithOptions creates a new MCP server from the given options.
func NewWithOptions(opts Options) *Server {
	tb := tools.NewToolboxWithOptions(tools.Options{
		WorkingDir: opts.WorkingDir,
		LSPServers: opts.LSPServers,
	})
	s := &Server{
		toolbox:   tb,
		registry:  buildRegistry(tb),
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
