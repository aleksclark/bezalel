//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
)

// lspImageOnce guards building the (expensive) real-LSP image so it happens at
// most once per `go test` invocation.
var (
	lspImageOnce sync.Once
	lspImageName string
)

// lspImage returns the tag of a bezalel image bundling real language servers,
// building it from Dockerfile.lsp unless BEZALEL_LSP_IMAGE is set.
func lspImage(t *testing.T) string {
	t.Helper()
	lspImageOnce.Do(func() {
		if img := os.Getenv("BEZALEL_LSP_IMAGE"); img != "" {
			lspImageName = img
			return
		}
		lspImageName = "bezalel:e2e-lsp"
		// Build from the repo root so the Dockerfile.lsp build context includes
		// the full source tree.
		cmd := exec.Command("docker", "build", "-f", "Dockerfile.lsp", "-t", lspImageName, ".")
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker build Dockerfile.lsp: %v\n%s", err, out)
		}
	})
	if lspImageName == "" {
		t.Fatal("failed to resolve LSP image")
	}
	return lspImageName
}

// startRealLSPHarness starts a container from the real-LSP image with the given
// workspace contents already written by the caller.
func startRealLSPHarness(t *testing.T, seed func(workspace string)) *harness {
	t.Helper()
	image := lspImage(t)

	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatalf("chmod workspace: %v", err)
	}
	seed(workspace)
	return runContainerImage(t, workspace, image)
}

// pollDiagnostics calls lsp_diagnostics repeatedly until the output contains
// want, accommodating the warm-up time real language servers need before they
// publish (and sometimes re-publish) diagnostics.
func pollDiagnostics(ctx context.Context, t *testing.T, c *client.Client, file, want string) string {
	t.Helper()
	var last string
	deadline := time.Now().Add(150 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		res := callTool(ctx, t, c, "lsp_diagnostics", map[string]any{"file_path": file})
		last = firstText(t, res)
		if !res.IsError && strings.Contains(last, want) {
			return last
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("did not observe %q in diagnostics for %s within timeout; last output:\n%s", want, file, last)
	return ""
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

const goplsLSPConfig = `lsp:
  - name: gopls
    command: gopls
    extensions:
      - ".go"
    root_markers:
      - "go.mod"
    language_id: go
    env:
      - "HOME=/tmp"
      - "GOPATH=/tmp/go"
      - "GOCACHE=/tmp/.cache/go-build"
      - "GOMODCACHE=/tmp/go/pkg/mod"
      - "GOFLAGS=-mod=mod"
      - "GOTOOLCHAIN=local"
`

const tsLSPConfig = `lsp:
  - name: typescript
    command: typescript-language-server
    args:
      - "--stdio"
    extensions:
      - ".ts"
      - ".tsx"
    root_markers:
      - "tsconfig.json"
      - "package.json"
    language_id: typescript
    env:
      - "HOME=/tmp"
      - "XDG_CACHE_HOME=/tmp/.cache"
`

func TestRealGoplsDiagnostics(t *testing.T) {
	h := startRealLSPHarness(t, func(ws string) {
		writeFile(t, ws, "bezalel.yaml", goplsLSPConfig)
		writeFile(t, ws, "go.mod", "module example.com/buggy\n\ngo 1.21\n")
		// A genuine type error: assigning a string constant to an int variable.
		writeFile(t, ws, "main.go", "package main\n\nfunc main() {\n\tvar n int = \"not a number\"\n\t_ = n\n}\n")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	out := pollDiagnostics(ctx, t, c, "main.go", "main.go")
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "int") || !strings.Contains(lower, "string") {
		t.Errorf("expected a gopls type-mismatch diagnostic mentioning int/string:\n%s", out)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("expected an error-severity diagnostic:\n%s", out)
	}
}

func TestRealGoplsReferences(t *testing.T) {
	h := startRealLSPHarness(t, func(ws string) {
		writeFile(t, ws, "bezalel.yaml", goplsLSPConfig)
		writeFile(t, ws, "go.mod", "module example.com/refs\n\ngo 1.21\n")
		src := "package main\n\n" +
			"import \"fmt\"\n\n" +
			"func greet() string { return \"hi\" }\n\n" +
			"func main() {\n" +
			"\tfmt.Println(greet())\n" +
			"\tfmt.Println(greet())\n" +
			"}\n"
		writeFile(t, ws, "main.go", src)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// gopls can take time to index; retry until references resolve.
	var out string
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		res := callTool(ctx, t, c, "lsp_references", map[string]any{"symbol": "greet"})
		out = firstText(t, res)
		if !res.IsError && strings.Contains(out, "main.go") && strings.Contains(out, "reference(s)") {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !strings.Contains(out, "main.go") {
		t.Fatalf("expected references in main.go:\n%s", out)
	}
	// Declaration + two call sites = three references.
	if !strings.Contains(out, "3 reference(s)") {
		t.Errorf("expected 3 references to greet, got:\n%s", out)
	}
}

func TestRealTypescriptDiagnostics(t *testing.T) {
	h := startRealLSPHarness(t, func(ws string) {
		writeFile(t, ws, "bezalel.yaml", tsLSPConfig)
		writeFile(t, ws, "tsconfig.json", "{\n  \"compilerOptions\": {\n    \"strict\": true,\n    \"noEmit\": true\n  },\n  \"include\": [\"*.ts\"]\n}\n")
		// Type error: a string is not assignable to a number.
		writeFile(t, ws, "bad.ts", "const x: number = \"hello\";\nexport { x };\n")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	out := pollDiagnostics(ctx, t, c, "bad.ts", "bad.ts")
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "number") || !strings.Contains(lower, "assignable") {
		t.Errorf("expected a TypeScript 'not assignable to number' diagnostic:\n%s", out)
	}
}

func TestRealLSPRestart(t *testing.T) {
	h := startRealLSPHarness(t, func(ws string) {
		writeFile(t, ws, "bezalel.yaml", goplsLSPConfig)
		writeFile(t, ws, "go.mod", "module example.com/restart\n\ngo 1.21\n")
		writeFile(t, ws, "main.go", "package main\n\nfunc main() {\n\tvar n int = \"bad\"\n\t_ = n\n}\n")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// Warm up gopls and confirm it reports the error.
	pollDiagnostics(ctx, t, c, "main.go", "error")

	// Restart the real server.
	res := callTool(ctx, t, c, "lsp_restart", map[string]any{"name": "gopls"})
	if res.IsError {
		t.Fatalf("lsp_restart error: %s", firstText(t, res))
	}
	if !strings.Contains(firstText(t, res), "Restarted") {
		t.Errorf("expected restart confirmation:\n%s", firstText(t, res))
	}

	// It should relaunch lazily and report the error again.
	out := pollDiagnostics(ctx, t, c, "main.go", "error")
	if !strings.Contains(strings.ToLower(out), "int") {
		t.Errorf("expected diagnostics after restart:\n%s", out)
	}
}
