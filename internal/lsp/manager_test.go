package lsp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aleksclark/bezalel/internal/lsp"
)

func newManager(workDir string) *lsp.Manager {
	return lsp.NewManager(workDir, []lsp.ServerConfig{
		{
			Name:        "gopls",
			Command:     "gopls",
			Extensions:  []string{".go"},
			RootMarkers: []string{"go.mod"},
			LanguageID:  "go",
		},
		{
			Name:       "pylsp",
			Command:    "pylsp",
			Extensions: []string{".py", ".pyi"},
			LanguageID: "python",
		},
	})
}

func TestManagerConfigured(t *testing.T) {
	if lsp.NewManager(".", nil).Configured() {
		t.Error("expected Configured()=false with no servers")
	}
	if !newManager(".").Configured() {
		t.Error("expected Configured()=true with servers")
	}
}

func TestManagerNamesAndExtensions(t *testing.T) {
	m := newManager(".")
	names := m.Names()
	if len(names) != 2 || names[0] != "gopls" || names[1] != "pylsp" {
		t.Errorf("unexpected names: %v", names)
	}
	exts := m.Extensions()
	want := map[string]bool{".go": true, ".py": true, ".pyi": true}
	if len(exts) != 3 {
		t.Errorf("expected 3 extensions, got %v", exts)
	}
	for _, e := range exts {
		if !want[e] {
			t.Errorf("unexpected extension %q", e)
		}
	}
}

func TestManagerRestartNoClients(t *testing.T) {
	m := newManager(".")
	restarted, err := m.Restart("")
	if err != nil {
		t.Fatalf("restart all: %v", err)
	}
	if len(restarted) != 0 {
		t.Errorf("expected no restarted servers, got %v", restarted)
	}
}

func TestManagerRestartUnknown(t *testing.T) {
	m := newManager(".")
	if _, err := m.Restart("ghc"); err == nil {
		t.Error("expected error restarting unknown server")
	}
}

func TestManagerRunningEmpty(t *testing.T) {
	m := newManager(".")
	if got := m.Running(); len(got) != 0 {
		t.Errorf("expected no running servers, got %v", got)
	}
}

func TestURIToPathRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "some file.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Build a file URI the same way the client does and convert back.
	uri := "file://" + p
	if got := lsp.URIToPath(uri); got != p {
		t.Errorf("URIToPath(%q) = %q, want %q", uri, got, p)
	}
}
