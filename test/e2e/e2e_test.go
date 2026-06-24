//go:build e2e

// Package e2e runs the bezalel MCP server inside a Docker container and
// exercises every tool through a standard MCP client (mark3labs/mcp-go),
// validating side effects against the mounted Docker volume on the host.
//
// Run with:
//
//	go test -tags e2e -v ./test/e2e/...
//
// Requirements: a working Docker daemon. The image is built automatically
// unless BEZALEL_IMAGE is set to a prebuilt tag.
package e2e

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const authToken = "e2e-secret-token"

// harness is a running bezalel container plus its host-side workspace volume.
type harness struct {
	baseURL   string // http://127.0.0.1:PORT
	workspace string // host path mounted at /workspace
	container string
}

// firstText returns the first text content block of a tool result.
func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("tool result has no content: %+v", res)
	}
	tc, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("first content block is not text: %T", res.Content[0])
	}
	return tc.Text
}

// callTool is a convenience wrapper around client.CallTool.
func callTool(ctx context.Context, t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func newClient(t *testing.T, baseURL, token string) *client.Client {
	t.Helper()
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	c, err := client.NewStreamableHttpClient(
		baseURL+"/mcp",
		transport.WithHTTPHeaders(headers),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func initClient(ctx context.Context, t *testing.T, c *client.Client) {
	t.Helper()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = "2024-11-05"
	initReq.Params.ClientInfo = mcp.Implementation{Name: "e2e-test", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
}

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "skipping e2e: docker not found in PATH")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startHarness builds (if needed) and runs the bezalel container.
func startHarness(t *testing.T) *harness {
	t.Helper()

	image := os.Getenv("BEZALEL_IMAGE")
	if image == "" {
		image = "bezalel:e2e"
		buildImage(t, image)
	}

	workspace := t.TempDir()
	// Container runs as root; ensure it can write into the mounted dir.
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatalf("chmod workspace: %v", err)
	}

	runArgs := []string{
		"run", "-d", "--rm",
		"-p", "127.0.0.1::8080",
		"-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "BEZALEL_AUTH_TOKEN=" + authToken,
		"-v", workspace + ":/workspace",
		image,
	}
	out, err := exec.Command("docker", runArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	container := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})

	port := containerPort(t, container, "8080")
	h := &harness{
		baseURL:   "http://127.0.0.1:" + port,
		workspace: workspace,
		container: container,
	}

	waitHealthy(t, h)
	return h
}

func buildImage(t *testing.T, tag string) {
	t.Helper()
	// Build from the repo root (two levels up from test/e2e).
	cmd := exec.Command("docker", "build", "-t", tag, "../..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
}

func containerPort(t *testing.T, container, internal string) string {
	t.Helper()
	out, err := exec.Command("docker", "port", container, internal).CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, out)
	}
	// Output looks like: 0.0.0.0:49154\n[::]:49154
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		t.Fatalf("unexpected docker port output: %q", out)
	}
	return line[idx+1:]
}

func waitHealthy(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.baseURL + "/health")
		if err == nil {
			resp.Body.Close() //nolint:errcheck
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", h.container).CombinedOutput()
	t.Fatalf("server did not become healthy in time\ncontainer logs:\n%s", logs)
}

func TestServerStartsAndWarnsWithoutToken(t *testing.T) {
	image := os.Getenv("BEZALEL_IMAGE")
	if image == "" {
		image = "bezalel:e2e"
		buildImage(t, image)
	}

	// Run without an auth token; server should warn but still start.
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::8080", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	container := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", container).Run() //nolint:errcheck

	port := containerPort(t, container, "8080")
	h := &harness{baseURL: "http://127.0.0.1:" + port, container: container}
	waitHealthy(t, h)

	logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
	if !strings.Contains(string(logs), "no auth token configured") {
		t.Errorf("expected startup warning about missing auth token, got logs:\n%s", logs)
	}
}

func TestAuthRejectsBadToken(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, "wrong-token")
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = "2024-11-05"
	initReq.Params.ClientInfo = mcp.Implementation{Name: "e2e-test", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err == nil {
		t.Fatal("expected initialize to fail with bad token, got nil error")
	}
}

func TestToolsListExposesAllTools(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	want := []string{"bash", "job_output", "job_kill", "view", "write", "edit", "delete", "ls", "glob", "grep"}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tools/list missing %q (got %v)", name, got)
		}
	}
}

func TestWriteThenReadFromVolume(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	const content = "hello from mcp\nsecond line\n"
	res := callTool(ctx, t, c, "write", map[string]any{
		"file_path": "notes/output.txt",
		"content":   content,
	})
	if res.IsError {
		t.Fatalf("write returned error: %s", firstText(t, res))
	}

	// Validate by reading the file directly from the host-mounted volume.
	hostPath := h.workspace + "/notes/output.txt"
	data, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("read file from volume %s: %v", hostPath, err)
	}
	if string(data) != content {
		t.Errorf("volume content mismatch:\n got: %q\nwant: %q", string(data), content)
	}
}

func TestViewReadsHostFile(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// Seed a file on the host volume; the container should see it.
	if err := os.WriteFile(h.workspace+"/seed.txt", []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res := callTool(ctx, t, c, "view", map[string]any{"file_path": "seed.txt"})
	if res.IsError {
		t.Fatalf("view error: %s", firstText(t, res))
	}
	text := firstText(t, res)
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(text, want) {
			t.Errorf("view output missing %q:\n%s", want, text)
		}
	}
}

func TestEditUpdatesVolumeFile(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	if err := os.WriteFile(h.workspace+"/config.txt", []byte("mode=off\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res := callTool(ctx, t, c, "edit", map[string]any{
		"file_path":  "config.txt",
		"old_string": "mode=off",
		"new_string": "mode=on",
	})
	if res.IsError {
		t.Fatalf("edit error: %s", firstText(t, res))
	}

	data, err := os.ReadFile(h.workspace + "/config.txt")
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "mode=on" {
		t.Errorf("edit did not apply, file contains: %q", string(data))
	}
}

func TestDeleteRemovesVolumeFile(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	target := h.workspace + "/trash.txt"
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res := callTool(ctx, t, c, "delete", map[string]any{"file_path": "trash.txt"})
	if res.IsError {
		t.Fatalf("delete error: %s", firstText(t, res))
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted, stat err = %v", err)
	}
}

func TestLsGlobGrep(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// Seed a small tree on the host volume.
	mustWrite(t, h.workspace+"/src/main.go", "package main\n\nfunc needle() {}\n")
	mustWrite(t, h.workspace+"/src/util.go", "package main\n")
	mustWrite(t, h.workspace+"/README.md", "# title\n")

	lsRes := callTool(ctx, t, c, "ls", map[string]any{})
	if lsRes.IsError {
		t.Fatalf("ls error: %s", firstText(t, lsRes))
	}
	if !strings.Contains(firstText(t, lsRes), "src") {
		t.Errorf("ls output missing 'src':\n%s", firstText(t, lsRes))
	}

	globRes := callTool(ctx, t, c, "glob", map[string]any{"pattern": "**/*.go"})
	if globRes.IsError {
		t.Fatalf("glob error: %s", firstText(t, globRes))
	}
	globText := firstText(t, globRes)
	if !strings.Contains(globText, "main.go") || !strings.Contains(globText, "util.go") {
		t.Errorf("glob did not find go files:\n%s", globText)
	}

	grepRes := callTool(ctx, t, c, "grep", map[string]any{"pattern": "needle", "include": "*.go"})
	if grepRes.IsError {
		t.Fatalf("grep error: %s", firstText(t, grepRes))
	}
	if !strings.Contains(firstText(t, grepRes), "main.go") {
		t.Errorf("grep did not locate 'needle' in main.go:\n%s", firstText(t, grepRes))
	}
}

func TestBashExecutesAndWritesVolume(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// Echo to stdout.
	res := callTool(ctx, t, c, "bash", map[string]any{"command": "echo bezalel-ok"})
	if res.IsError {
		t.Fatalf("bash error: %s", firstText(t, res))
	}
	if !strings.Contains(firstText(t, res), "bezalel-ok") {
		t.Errorf("bash stdout missing marker:\n%s", firstText(t, res))
	}

	// Write a file via bash and confirm on the host volume.
	res = callTool(ctx, t, c, "bash", map[string]any{
		"command": "echo from-shell > /workspace/shell-out.txt",
	})
	if res.IsError {
		t.Fatalf("bash write error: %s", firstText(t, res))
	}
	data, err := os.ReadFile(h.workspace + "/shell-out.txt")
	if err != nil {
		t.Fatalf("read shell output from volume: %v", err)
	}
	if strings.TrimSpace(string(data)) != "from-shell" {
		t.Errorf("unexpected shell output file content: %q", string(data))
	}
}

func TestBashBackgroundJobLifecycle(t *testing.T) {
	h := startHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newClient(t, h.baseURL, authToken)
	initClient(ctx, t, c)

	// Start a long-running background job.
	res := callTool(ctx, t, c, "bash", map[string]any{
		"command":           "for i in 1 2 3 4 5; do echo tick-$i; sleep 1; done",
		"run_in_background": true,
	})
	if res.IsError {
		t.Fatalf("background bash error: %s", firstText(t, res))
	}
	jobID := parseJobID(t, firstText(t, res))

	// Poll output.
	out := callTool(ctx, t, c, "job_output", map[string]any{"job_id": jobID})
	if out.IsError {
		t.Fatalf("job_output error: %s", firstText(t, out))
	}
	if !strings.Contains(firstText(t, out), "tick-") {
		t.Errorf("expected partial job output, got:\n%s", firstText(t, out))
	}

	// Kill it.
	kill := callTool(ctx, t, c, "job_kill", map[string]any{"job_id": jobID})
	if kill.IsError {
		t.Fatalf("job_kill error: %s", firstText(t, kill))
	}
	if !strings.Contains(firstText(t, kill), jobID) {
		t.Errorf("job_kill response missing job id %q:\n%s", jobID, firstText(t, kill))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(dir(path), 0o777); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func dir(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

// parseJobID extracts a job ID from a "Background job ... ID: XXX" message.
func parseJobID(t *testing.T, text string) string {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if idx := strings.LastIndex(line, "ID: "); idx >= 0 {
			id := strings.TrimSpace(line[idx+len("ID: "):])
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("could not parse job id from:\n%s", text)
	return ""
}
