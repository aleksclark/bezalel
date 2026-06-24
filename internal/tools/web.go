// Package tools - network fetch/download tool implementations for bezalel.
package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aleksclark/bezalel/internal/version"
)

const (
	// maxFetchBytes caps how much of a response body fetch/web_fetch will read.
	maxFetchBytes = 5 * 1024 * 1024 // 5MB
	// maxInlineFetch is the size above which fetch truncates inline content
	// and web_fetch spills to a temp file.
	maxInlineFetch = 50 * 1024 // 50KB
	// defaultNetTimeout is the default HTTP timeout when none is supplied.
	defaultNetTimeout = 120 * time.Second
	// maxNetTimeout is the maximum allowed HTTP timeout.
	maxNetTimeout = 600 * time.Second
)

// DownloadParams are the parameters for the download tool.
type DownloadParams struct {
	URL      string `json:"url"`
	FilePath string `json:"file_path"`
	Timeout  int    `json:"timeout,omitempty"`
}

// FetchParams are the parameters for the fetch tool.
type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// WebFetchParams are the parameters for the web_fetch tool.
type WebFetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

func resolveTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultNetTimeout
	}
	d := time.Duration(seconds) * time.Second
	if d > maxNetTimeout {
		return maxNetTimeout
	}
	return d
}

func validateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q (only http and https are allowed)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url has no host")
	}
	return nil
}

func httpGet(ctx context.Context, rawURL string, timeout time.Duration) (*http.Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	// #nosec G107 -- fetching caller-supplied URLs is the purpose of these tools.
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", version.UserAgent())
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("request failed: %w", err)
	}
	// Attach cancel to body close via a wrapper.
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// Download streams a URL to a local file.
func (t *Toolbox) Download(ctx context.Context, params DownloadParams) (string, error) {
	if err := validateURL(params.URL); err != nil {
		return "", err
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	filePath := t.resolvePath(params.FilePath)

	resp, err := httpGet(ctx, params.URL, resolveTimeout(params.Timeout))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download failed: HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	f, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot create file: %w", err)
	}

	n, err := io.Copy(f, resp.Body)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("write failed: %w", err)
	}

	return fmt.Sprintf("Downloaded %s to %s (%d bytes)", params.URL, params.FilePath, n), nil
}

// Fetch retrieves a URL and returns its content inline as text, markdown, or html.
func (t *Toolbox) Fetch(ctx context.Context, params FetchParams) (string, error) {
	body, contentType, err := t.fetchBody(ctx, params.URL, params.Timeout)
	if err != nil {
		return "", err
	}

	content := convertContent(body, contentType, params.Format)
	if len(content) > maxInlineFetch {
		truncated := content[:maxInlineFetch]
		return fmt.Sprintf("%s\n\n... [content truncated at %d bytes of %d total]", truncated, maxInlineFetch, len(content)), nil
	}
	return content, nil
}

// WebFetch behaves like Fetch but spills oversized content to a temp file in
// the working directory and returns the path instead of inline content.
func (t *Toolbox) WebFetch(ctx context.Context, params WebFetchParams) (string, error) {
	body, contentType, err := t.fetchBody(ctx, params.URL, params.Timeout)
	if err != nil {
		return "", err
	}

	content := convertContent(body, contentType, params.Format)
	if len(content) <= maxInlineFetch {
		return content, nil
	}

	ext := ".txt"
	switch normalizeFormat(params.Format) {
	case "html":
		ext = ".html"
	case "markdown":
		ext = ".md"
	}

	f, err := os.CreateTemp(t.shellMgr.WorkingDir(), "webfetch-*"+ext)
	if err != nil {
		return "", fmt.Errorf("cannot create temp file: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("cannot write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("cannot close temp file: %w", err)
	}

	return fmt.Sprintf("Content too large to return inline (%d bytes). Saved to %s", len(content), f.Name()), nil
}

func (t *Toolbox) fetchBody(ctx context.Context, rawURL string, timeout int) (body, contentType string, err error) {
	if vErr := validateURL(rawURL); vErr != nil {
		return "", "", vErr
	}

	resp, err := httpGet(ctx, rawURL, resolveTimeout(timeout))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch failed: HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}
	return string(data), resp.Header.Get("Content-Type"), nil
}

func normalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "markdown", "md":
		return "markdown"
	case "text", "txt", "plain":
		return "text"
	case "html":
		return "html"
	default:
		return "markdown"
	}
}

func convertContent(body, contentType, format string) string {
	f := normalizeFormat(format)
	if f == "html" {
		return body
	}

	isHTML := strings.Contains(strings.ToLower(contentType), "html") || looksLikeHTML(body)
	if !isHTML {
		// Non-HTML payloads (JSON, plain text) are returned as-is.
		return body
	}

	if f == "text" {
		return htmlToText(body)
	}
	return htmlToMarkdown(body)
}

func looksLikeHTML(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "<html") || strings.Contains(lower, "<body") || strings.Contains(lower, "<!doctype html")
}

var (
	reScript     = regexp.MustCompile(`(?is)<script[^>]*>.*?</\s*script\s*>`)
	reStyle      = regexp.MustCompile(`(?is)<style[^>]*>.*?</\s*style\s*>`)
	reComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reTag        = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
	reSpaces     = regexp.MustCompile(`[ \t]{2,}`)
	reHeading    = regexp.MustCompile(`(?is)<h([1-6])[^>]*>(.*?)</\s*h[1-6]\s*>`)
	reAnchor     = regexp.MustCompile(`(?is)<a\s+[^>]*href\s*=\s*["']([^"']*)["'][^>]*>(.*?)</\s*a\s*>`)
	reListItem   = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</\s*li\s*>`)
)

func htmlToText(body string) string {
	s := reStyle.ReplaceAllString(reScript.ReplaceAllString(body, ""), "")
	s = reComment.ReplaceAllString(s, "")
	// Block elements become newlines.
	s = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr)\s*/?>`).ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = unescapeHTML(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func htmlToMarkdown(body string) string {
	s := reStyle.ReplaceAllString(reScript.ReplaceAllString(body, ""), "")
	s = reComment.ReplaceAllString(s, "")

	s = reHeading.ReplaceAllStringFunc(s, func(m string) string {
		groups := reHeading.FindStringSubmatch(m)
		level := len(groups[1])
		text := strings.TrimSpace(reTag.ReplaceAllString(groups[2], ""))
		return "\n\n" + strings.Repeat("#", level) + " " + text + "\n\n"
	})

	s = reAnchor.ReplaceAllStringFunc(s, func(m string) string {
		groups := reAnchor.FindStringSubmatch(m)
		href := groups[1]
		text := strings.TrimSpace(reTag.ReplaceAllString(groups[2], ""))
		if text == "" {
			text = href
		}
		return fmt.Sprintf("[%s](%s)", text, href)
	})

	s = reListItem.ReplaceAllStringFunc(s, func(m string) string {
		groups := reListItem.FindStringSubmatch(m)
		text := strings.TrimSpace(reTag.ReplaceAllString(groups[1], ""))
		return "\n- " + text
	})

	s = regexp.MustCompile(`(?i)<(br|/p|/div|/tr)\s*/?>`).ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = unescapeHTML(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func unescapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
	)
	return replacer.Replace(s)
}
