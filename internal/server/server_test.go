package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aleksclark/bezalel/internal/server"
)

// rpcCall sends a JSON-RPC request and returns the parsed response.
func rpcCall(t *testing.T, url, method string, params any) map[string]any {
	t.Helper()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(url+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("unmarshal response %q: %v", string(respBody), err)
	}
	return result
}

// rawPost sends raw bytes as POST and returns the parsed response.
func rawPost(t *testing.T, url string, body []byte) map[string]any {
	t.Helper()

	resp, err := http.Post(url+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("unmarshal response %q: %v", string(respBody), err)
	}
	return result
}

// getToolResult extracts the text content from a tools/call result.
func getToolResult(t *testing.T, resp map[string]any) (text string, isError bool) {
	t.Helper()

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T: %v", resp["result"], resp)
	}

	isErr, _ := result["isError"].(bool)

	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected content array, got: %v", result)
	}

	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content[0] map, got %T", content[0])
	}

	return first["text"].(string), isErr
}

func startServer(t *testing.T, workDir string) *httptest.Server {
	t.Helper()
	srv := server.New(workDir)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		srv.Shutdown()
	})
	return ts
}

func TestInitialize(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "0.1.0"},
	})

	if resp["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T", resp["result"])
	}

	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("expected capabilities map")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("capabilities missing 'tools' key")
	}

	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("expected serverInfo map")
	}
	if info["name"] != "bezalel" {
		t.Errorf("serverInfo.name = %v, want bezalel", info["name"])
	}
	if info["version"] != "0.1.0" {
		t.Errorf("serverInfo.version = %v, want 0.1.0", info["version"])
	}
}

func TestToolsList(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/list", nil)

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got: %v", resp)
	}

	toolsList, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array, got: %v", result)
	}

	if len(toolsList) < 3 {
		t.Errorf("expected at least 3 tools, got %d", len(toolsList))
	}

	// Verify expected tools exist
	toolNames := make(map[string]bool)
	for _, tool := range toolsList {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		toolNames[name] = true

		// Every tool should have a description and inputSchema
		if _, ok := tm["description"]; !ok {
			t.Errorf("tool %q missing description", name)
		}
		schema, ok := tm["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("tool %q missing inputSchema", name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q inputSchema.type = %v, want object", name, schema["type"])
		}
	}

	requiredTools := []string{"bash", "job_output", "job_kill"}
	for _, name := range requiredTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q not found in tools/list", name)
		}
	}
}

func TestBashEcho(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "echo hello world"},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("expected no error, got isError=true, text=%q", text)
	}
	if !strings.Contains(text, "hello world") {
		t.Errorf("expected output to contain 'hello world', got %q", text)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "exit 42"},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("non-zero exit should not set isError=true (it's a normal result)")
	}
	if !strings.Contains(text, "42") {
		t.Errorf("expected output to reference exit code 42, got %q", text)
	}
}

func TestBashWorkingDir(t *testing.T) {
	dir := t.TempDir()
	ts := startServer(t, dir)

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "pwd", "working_dir": dir},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("unexpected error: %q", text)
	}
	if !strings.Contains(text, dir) {
		t.Errorf("expected pwd output to contain %q, got %q", dir, text)
	}
}

func TestBashBackgroundJobFlow(t *testing.T) {
	ts := startServer(t, t.TempDir())

	// Start a background job
	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name": "bash",
		"arguments": map[string]any{
			"command":           "echo background-output && sleep 5",
			"run_in_background": true,
		},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Fatalf("expected no error starting background job, got: %q", text)
	}
	if !strings.Contains(text, "Background job started") {
		t.Fatalf("expected background job message, got: %q", text)
	}

	// Extract job ID from text (format: "Background job started with ID: XXX")
	var jobID string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "ID:") {
			parts := strings.Split(line, "ID: ")
			if len(parts) >= 2 {
				jobID = strings.TrimSpace(parts[1])
				break
			}
		}
	}
	if jobID == "" {
		t.Fatalf("could not extract job ID from: %q", text)
	}

	// Poll job_output
	time.Sleep(500 * time.Millisecond)
	resp = rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "job_output",
		"arguments": map[string]any{"job_id": jobID},
	})

	text, isErr = getToolResult(t, resp)
	if isErr {
		t.Errorf("job_output returned error: %q", text)
	}
	if !strings.Contains(text, "running") && !strings.Contains(text, "completed") {
		t.Errorf("expected status in job_output, got: %q", text)
	}
	if !strings.Contains(text, "background-output") {
		t.Errorf("expected 'background-output' in job output, got: %q", text)
	}

	// Kill the job
	resp = rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "job_kill",
		"arguments": map[string]any{"job_id": jobID},
	})

	text, isErr = getToolResult(t, resp)
	if isErr {
		t.Errorf("job_kill returned error: %q", text)
	}
	if !strings.Contains(text, "terminated") {
		t.Errorf("expected terminated message, got: %q", text)
	}
}

func TestBashUnknownTool(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})

	text, isErr := getToolResult(t, resp)
	if !isErr {
		t.Errorf("expected isError=true for unknown tool, got false")
	}
	if !strings.Contains(text, "Unknown tool") {
		t.Errorf("expected 'Unknown tool' in error, got: %q", text)
	}
}

func TestInvalidJSON(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rawPost(t, ts.URL, []byte(`{this is not valid json`))

	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error in response, got: %v", resp)
	}

	code, _ := rpcErr["code"].(float64)
	if code != -32700 {
		t.Errorf("expected error code -32700, got %v", code)
	}
	msg, _ := rpcErr["message"].(string)
	if !strings.Contains(msg, "Parse error") {
		t.Errorf("expected 'Parse error' in message, got %q", msg)
	}
}

func TestMethodNotFound(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "unknown/method", nil)

	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error in response, got: %v", resp)
	}

	code, _ := rpcErr["code"].(float64)
	if code != -32601 {
		t.Errorf("expected error code -32601, got %v", code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health status = %q, want ok", body["status"])
	}
}

func TestBashEmptyCommand(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": ""},
	})

	text, isErr := getToolResult(t, resp)
	if !isErr {
		t.Errorf("expected isError=true for empty command, got false")
	}
	if !strings.Contains(text, "required") {
		t.Errorf("expected error about command being required, got: %q", text)
	}
}

func TestBashMultilineOutput(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "echo line1; echo line2; echo line3"},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("unexpected error: %q", text)
	}
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line2") || !strings.Contains(text, "line3") {
		t.Errorf("expected multiline output, got: %q", text)
	}
}

func TestBashStderrOutput(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "echo stderr-msg >&2"},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("unexpected isError=true for stderr output")
	}
	if !strings.Contains(text, "stderr-msg") {
		t.Errorf("expected stderr in output, got: %q", text)
	}
}

func TestBashFileCreation(t *testing.T) {
	dir := t.TempDir()
	ts := startServer(t, dir)

	// Create a file via bash
	filePath := filepath.Join(dir, "test.txt")
	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": fmt.Sprintf("echo 'hello file' > %s", filePath)},
	})

	_, isErr := getToolResult(t, resp)
	if isErr {
		t.Fatalf("unexpected error creating file")
	}

	// Verify file exists
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(content), "hello file") {
		t.Errorf("file content = %q, want 'hello file'", string(content))
	}
}

func TestBashPipeline(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "bash",
		"arguments": map[string]any{"command": "echo 'apple banana cherry' | tr ' ' '\\n' | sort | head -1"},
	})

	text, isErr := getToolResult(t, resp)
	if isErr {
		t.Errorf("unexpected error: %q", text)
	}
	if !strings.Contains(text, "apple") {
		t.Errorf("expected 'apple' in sorted output, got: %q", text)
	}
}

func TestJobOutputNonexistentJob(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "job_output",
		"arguments": map[string]any{"job_id": "NONEXISTENT"},
	})

	text, isErr := getToolResult(t, resp)
	if !isErr {
		t.Errorf("expected isError=true for nonexistent job")
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error, got: %q", text)
	}
}

func TestJobKillNonexistentJob(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
		"name":      "job_kill",
		"arguments": map[string]any{"job_id": "NONEXISTENT"},
	})

	text, isErr := getToolResult(t, resp)
	if !isErr {
		t.Errorf("expected isError=true for nonexistent job")
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error, got: %q", text)
	}
}

func TestInvalidJSONRPCVersion(t *testing.T) {
	ts := startServer(t, t.TempDir())

	body := map[string]any{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  "initialize",
	}
	data, _ := json.Marshal(body)

	resp := rawPost(t, ts.URL, data)

	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error in response, got: %v", resp)
	}

	code, _ := rpcErr["code"].(float64)
	if code != -32600 {
		t.Errorf("expected error code -32600, got %v", code)
	}
}

func TestConcurrentRequests(t *testing.T) {
	ts := startServer(t, t.TempDir())

	// Send multiple concurrent requests
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			resp := rpcCall(t, ts.URL, "tools/call", map[string]any{
				"name":      "bash",
				"arguments": map[string]any{"command": fmt.Sprintf("echo concurrent-%d", n)},
			})
			text, isErr := getToolResult(t, resp)
			if isErr {
				done <- fmt.Errorf("request %d got error: %s", n, text)
				return
			}
			if !strings.Contains(text, fmt.Sprintf("concurrent-%d", n)) {
				done <- fmt.Errorf("request %d: expected 'concurrent-%d' in output, got %q", n, n, text)
				return
			}
			done <- nil
		}(i)
	}

	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	srv := server.NewWithOptions(server.Options{WorkingDir: t.TempDir(), AuthToken: "s3cret"})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		srv.Shutdown()
	})

	if !srv.AuthEnabled() {
		t.Fatal("expected AuthEnabled to be true")
	}

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// No token -> 401
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}

	// Wrong token -> 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", resp.StatusCode)
	}

	// Correct token -> 200
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200", resp.StatusCode)
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	ts := startServer(t, t.TempDir())

	resp := rpcCall(t, ts.URL, "tools/list", nil)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("expected result without auth, got: %v", resp)
	}
}
