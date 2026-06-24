package server_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleksclark/bezalel/internal/lsp"
	"github.com/aleksclark/bezalel/internal/server"
	"github.com/aleksclark/bezalel/test/fakelsp"
)

// TestMain lets the test binary re-exec itself as a fake language server when
// BEZALEL_FAKE_LSP=1 is set, so LSP tools can be exercised end-to-end without a
// real language server installed.
func TestMain(m *testing.M) {
	if os.Getenv("BEZALEL_FAKE_LSP") == "1" {
		fakelsp.Run()
		return
	}
	os.Exit(m.Run())
}

func startServerWithLSP(t *testing.T, workDir string) *httptest.Server {
	t.Helper()
	srv := server.NewWithOptions(server.Options{
		WorkingDir: workDir,
		LSPServers: []lsp.ServerConfig{
			{
				Name:       "fake",
				Command:    os.Args[0],
				Env:        []string{"BEZALEL_FAKE_LSP=1"},
				Extensions: []string{".txt"},
				LanguageID: "plaintext",
			},
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		srv.Shutdown()
	})
	return ts
}

func TestLspToolsListedWithoutConfig(t *testing.T) {
	ts := startServer(t, t.TempDir())
	resp := rpcCall(t, ts.URL, "tools/list", nil)
	result := resp["result"].(map[string]any)
	toolsList := result["tools"].([]any)

	got := map[string]bool{}
	for _, tool := range toolsList {
		got[tool.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"lsp_diagnostics", "lsp_references", "lsp_restart"} {
		if !got[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}
}

func TestLspDiagnosticsWithoutConfig(t *testing.T) {
	ts := startServer(t, t.TempDir())
	text, isErr := callTool(t, ts.URL, "lsp_diagnostics", map[string]any{"file_path": "x.txt"})
	if !isErr {
		t.Fatalf("expected error when no servers configured, got: %s", text)
	}
	if !strings.Contains(text, "no language servers") {
		t.Errorf("unexpected message: %s", text)
	}
}

func TestLspDiagnosticsReportsProblems(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	if err := os.WriteFile(filepath.Join(workDir, "bad.txt"), []byte("line one\nthis has a BUG in it\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "lsp_diagnostics", map[string]any{"file_path": "bad.txt"})
	if isErr {
		t.Fatalf("lsp_diagnostics error: %s", text)
	}
	if !strings.Contains(text, "found BUG marker") {
		t.Errorf("expected diagnostic in output:\n%s", text)
	}
	if !strings.Contains(text, "error") {
		t.Errorf("expected severity label in output:\n%s", text)
	}
	if !strings.Contains(text, "2:12") {
		t.Errorf("expected 1-based line:col 2:12 in output:\n%s", text)
	}
}

func TestLspDiagnosticsClean(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	if err := os.WriteFile(filepath.Join(workDir, "ok.txt"), []byte("all good here\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "lsp_diagnostics", map[string]any{"file_path": "ok.txt"})
	if isErr {
		t.Fatalf("lsp_diagnostics error: %s", text)
	}
	if !strings.Contains(text, "No problems") {
		t.Errorf("expected clean result, got:\n%s", text)
	}
}

func TestLspDiagnosticsProjectWide(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("clean\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "b.txt"), []byte("has BUG here\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "lsp_diagnostics", map[string]any{})
	if isErr {
		t.Fatalf("lsp_diagnostics error: %s", text)
	}
	if !strings.Contains(text, "b.txt") || !strings.Contains(text, "found BUG marker") {
		t.Errorf("expected project-wide diagnostic for b.txt:\n%s", text)
	}
}

func TestLspReferences(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	if err := os.WriteFile(filepath.Join(workDir, "code.txt"), []byte("alpha beta\ngamma widget delta\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "lsp_references", map[string]any{"symbol": "widget"})
	if isErr {
		t.Fatalf("lsp_references error: %s", text)
	}
	if !strings.Contains(text, "code.txt") {
		t.Errorf("expected references to reference code.txt:\n%s", text)
	}
	if !strings.Contains(text, "reference(s)") {
		t.Errorf("expected reference count:\n%s", text)
	}
}

func TestLspReferencesSymbolNotFound(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	if err := os.WriteFile(filepath.Join(workDir, "code.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "lsp_references", map[string]any{"symbol": "missingsym"})
	if isErr {
		t.Fatalf("lsp_references error: %s", text)
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message:\n%s", text)
	}
}

func TestLspRestart(t *testing.T) {
	workDir := t.TempDir()
	ts := startServerWithLSP(t, workDir)

	// Restart before any server is running.
	text, isErr := callTool(t, ts.URL, "lsp_restart", map[string]any{})
	if isErr {
		t.Fatalf("lsp_restart error: %s", text)
	}
	if !strings.Contains(text, "start on next use") {
		t.Errorf("unexpected restart message (no servers running):\n%s", text)
	}

	// Start the server via a diagnostics call.
	if err := os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("BUG\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, isErr := callTool(t, ts.URL, "lsp_diagnostics", map[string]any{"file_path": "f.txt"}); isErr {
		t.Fatalf("diagnostics to start server failed")
	}

	// Now restart the named server.
	text, isErr = callTool(t, ts.URL, "lsp_restart", map[string]any{"name": "fake"})
	if isErr {
		t.Fatalf("lsp_restart named error: %s", text)
	}
	if !strings.Contains(text, "Restarted") {
		t.Errorf("expected restart confirmation:\n%s", text)
	}

	// Diagnostics should still work after restart (server relaunches lazily).
	text, isErr = callTool(t, ts.URL, "lsp_diagnostics", map[string]any{"file_path": "f.txt"})
	if isErr {
		t.Fatalf("post-restart diagnostics error: %s", text)
	}
	if !strings.Contains(text, "found BUG marker") {
		t.Errorf("expected diagnostics after restart:\n%s", text)
	}
}

func TestLspRestartUnknownServer(t *testing.T) {
	ts := startServerWithLSP(t, t.TempDir())
	text, isErr := callTool(t, ts.URL, "lsp_restart", map[string]any{"name": "nope"})
	if !isErr {
		t.Fatalf("expected error for unknown server, got: %s", text)
	}
	if !strings.Contains(text, "no language server named") {
		t.Errorf("unexpected message: %s", text)
	}
}
