package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// callTool is a helper that invokes a tool over JSON-RPC and returns the
// text content plus the isError flag.
func callTool(t *testing.T, url, name string, args map[string]any) (string, bool) {
	t.Helper()
	resp := rpcCall(t, url, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	return getToolResult(t, resp)
}

func TestMultiEditAppliesAllEdits(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)

	target := filepath.Join(workDir, "config.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "multiedit", map[string]any{
		"file_path": "config.txt",
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "ALPHA"},
			{"old_string": "gamma", "new_string": "GAMMA"},
		},
	})
	if isErr {
		t.Fatalf("multiedit returned error: %s", text)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if got := string(data); got != "ALPHA\nbeta\nGAMMA\n" {
		t.Errorf("unexpected content after multiedit: %q", got)
	}
}

func TestMultiEditReplaceAll(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)

	target := filepath.Join(workDir, "dup.txt")
	if err := os.WriteFile(target, []byte("x x x\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "multiedit", map[string]any{
		"file_path": "dup.txt",
		"edits": []map[string]any{
			{"old_string": "x", "new_string": "y", "replace_all": true},
		},
	})
	if isErr {
		t.Fatalf("multiedit returned error: %s", text)
	}

	data, _ := os.ReadFile(target)
	if got := string(data); got != "y y y\n" {
		t.Errorf("replace_all failed: %q", got)
	}
}

func TestMultiEditAtomicOnFailure(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)

	target := filepath.Join(workDir, "atomic.txt")
	const original = "one\ntwo\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Second edit references a string that does not exist; the whole op must fail
	// and leave the file unchanged.
	text, isErr := callTool(t, ts.URL, "multiedit", map[string]any{
		"file_path": "atomic.txt",
		"edits": []map[string]any{
			{"old_string": "one", "new_string": "ONE"},
			{"old_string": "missing", "new_string": "X"},
		},
	})
	if !isErr {
		t.Fatalf("expected error, got success: %s", text)
	}

	data, _ := os.ReadFile(target)
	if string(data) != original {
		t.Errorf("file was modified despite failed multiedit: %q", string(data))
	}
}

func TestMultiEditAmbiguousMatch(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)

	target := filepath.Join(workDir, "ambig.txt")
	if err := os.WriteFile(target, []byte("a a\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	text, isErr := callTool(t, ts.URL, "multiedit", map[string]any{
		"file_path": "ambig.txt",
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "b"},
		},
	})
	if !isErr {
		t.Fatalf("expected ambiguity error, got: %s", text)
	}
	if !strings.Contains(text, "matches") {
		t.Errorf("expected match-count error, got: %s", text)
	}
}

// contentServer returns an httptest server serving the given body/content-type.
func contentServer(t *testing.T, contentType, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadWritesFile(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)
	content := contentServer(t, "text/plain", "downloaded-bytes", http.StatusOK)

	text, isErr := callTool(t, ts.URL, "download", map[string]any{
		"url":       content.URL + "/file.txt",
		"file_path": "sub/out.bin",
	})
	if isErr {
		t.Fatalf("download error: %s", text)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "sub", "out.bin"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "downloaded-bytes" {
		t.Errorf("unexpected downloaded content: %q", string(data))
	}
	if !strings.Contains(text, "16 bytes") {
		t.Errorf("expected byte count in result, got: %s", text)
	}
}

func TestDownloadHTTPError(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)
	content := contentServer(t, "text/plain", "nope", http.StatusNotFound)

	text, isErr := callTool(t, ts.URL, "download", map[string]any{
		"url":       content.URL + "/missing",
		"file_path": "out.txt",
	})
	if !isErr {
		t.Fatalf("expected error for 404, got: %s", text)
	}
	if _, err := os.Stat(filepath.Join(workDir, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("file should not exist after failed download")
	}
}

func TestDownloadRejectsBadScheme(t *testing.T) {
	ts := startServer(t, t.TempDir())
	text, isErr := callTool(t, ts.URL, "download", map[string]any{
		"url":       "ftp://example.com/x",
		"file_path": "out.txt",
	})
	if !isErr {
		t.Fatalf("expected scheme error, got: %s", text)
	}
	if !strings.Contains(text, "scheme") {
		t.Errorf("expected scheme error message, got: %s", text)
	}
}

func TestFetchReturnsPlainContent(t *testing.T) {
	ts := startServer(t, t.TempDir())
	content := contentServer(t, "application/json", `{"status":"ok"}`, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "fetch", map[string]any{
		"url": content.URL + "/api",
	})
	if isErr {
		t.Fatalf("fetch error: %s", text)
	}
	if !strings.Contains(text, `"status":"ok"`) {
		t.Errorf("fetch did not return body: %s", text)
	}
}

func TestFetchHTMLToMarkdown(t *testing.T) {
	ts := startServer(t, t.TempDir())
	html := `<html><body><h1>Title</h1><p>Hello <a href="https://x.test">world</a></p></body></html>`
	content := contentServer(t, "text/html", html, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "fetch", map[string]any{
		"url":    content.URL + "/page",
		"format": "markdown",
	})
	if isErr {
		t.Fatalf("fetch error: %s", text)
	}
	if !strings.Contains(text, "# Title") {
		t.Errorf("expected markdown heading, got: %s", text)
	}
	if !strings.Contains(text, "[world](https://x.test)") {
		t.Errorf("expected markdown link, got: %s", text)
	}
}

func TestFetchHTMLToText(t *testing.T) {
	ts := startServer(t, t.TempDir())
	html := `<html><body><h1>Heading</h1><p>Body text</p><script>ignore()</script></body></html>`
	content := contentServer(t, "text/html", html, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "fetch", map[string]any{
		"url":    content.URL + "/page",
		"format": "text",
	})
	if isErr {
		t.Fatalf("fetch error: %s", text)
	}
	if !strings.Contains(text, "Heading") || !strings.Contains(text, "Body text") {
		t.Errorf("text extraction missing content: %s", text)
	}
	if strings.Contains(text, "ignore()") {
		t.Errorf("script content leaked into text output: %s", text)
	}
}

func TestFetchHTMLRawFormat(t *testing.T) {
	ts := startServer(t, t.TempDir())
	html := `<html><body><h1>Raw</h1></body></html>`
	content := contentServer(t, "text/html", html, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "fetch", map[string]any{
		"url":    content.URL + "/page",
		"format": "html",
	})
	if isErr {
		t.Fatalf("fetch error: %s", text)
	}
	if !strings.Contains(text, "<h1>Raw</h1>") {
		t.Errorf("html format should preserve tags, got: %s", text)
	}
}

func TestFetchTruncatesLargeContent(t *testing.T) {
	ts := startServer(t, t.TempDir())
	big := strings.Repeat("a", 60*1024)
	content := contentServer(t, "text/plain", big, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "fetch", map[string]any{
		"url": content.URL + "/big",
	})
	if isErr {
		t.Fatalf("fetch error: %s", text)
	}
	if !strings.Contains(text, "truncated") {
		t.Errorf("expected truncation notice, got tail: %s", text[len(text)-80:])
	}
}

func TestWebFetchSpillsLargeContentToFile(t *testing.T) {
	workDir := t.TempDir()
	ts := startServer(t, workDir)
	big := strings.Repeat("b", 60*1024)
	content := contentServer(t, "text/plain", big, http.StatusOK)

	text, isErr := callTool(t, ts.URL, "web_fetch", map[string]any{
		"url": content.URL + "/big",
	})
	if isErr {
		t.Fatalf("web_fetch error: %s", text)
	}
	if !strings.Contains(text, "Saved to") {
		t.Fatalf("expected spill-to-file message, got: %s", text)
	}

	// Parse the path and verify it exists with full content.
	idx := strings.LastIndex(text, "Saved to ")
	path := strings.TrimSpace(text[idx+len("Saved to "):])
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spilled file %q: %v", path, err)
	}
	if len(data) != len(big) {
		t.Errorf("spilled file size = %d, want %d", len(data), len(big))
	}
}

func TestWebFetchInlineWhenSmall(t *testing.T) {
	ts := startServer(t, t.TempDir())
	content := contentServer(t, "text/plain", "small body", http.StatusOK)

	text, isErr := callTool(t, ts.URL, "web_fetch", map[string]any{
		"url": content.URL + "/small",
	})
	if isErr {
		t.Fatalf("web_fetch error: %s", text)
	}
	if text != "small body" {
		t.Errorf("expected inline content, got: %s", text)
	}
}

func TestNewToolsInToolsList(t *testing.T) {
	ts := startServer(t, t.TempDir())
	resp := rpcCall(t, ts.URL, "tools/list", nil)
	result := resp["result"].(map[string]any)
	toolsList := result["tools"].([]any)

	got := map[string]bool{}
	for _, tool := range toolsList {
		tm := tool.(map[string]any)
		got[tm["name"].(string)] = true
	}
	for _, name := range []string{"multiedit", "download", "fetch", "web_fetch"} {
		if !got[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}
}
