// Package tools - LSP tool implementations for bezalel MCP server.
package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aleksclark/bezalel/internal/lsp"
)

const (
	diagnosticsWaitTimeout = 5 * time.Second
	maxProjectDiagFiles    = 200
	maxReferenceCandidates = 25
)

// LspDiagnosticsParams are the parameters for the lsp_diagnostics tool.
type LspDiagnosticsParams struct {
	FilePath string `json:"file_path,omitempty"`
}

// LspReferencesParams are the parameters for the lsp_references tool.
type LspReferencesParams struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path,omitempty"`
}

// LspRestartParams are the parameters for the lsp_restart tool.
type LspRestartParams struct {
	Name string `json:"name,omitempty"`
}

// LspDiagnostics returns language-server diagnostics for a file, or for every
// file matching a configured extension when file_path is empty.
func (t *Toolbox) LspDiagnostics(ctx context.Context, params LspDiagnosticsParams) (string, error) {
	if !t.lspMgr.Configured() {
		return "", fmt.Errorf("no language servers are configured")
	}

	if params.FilePath != "" {
		return t.diagnosticsForFile(ctx, params.FilePath)
	}
	return t.diagnosticsForProject(ctx)
}

func (t *Toolbox) diagnosticsForFile(ctx context.Context, path string) (string, error) {
	abs := t.resolvePath(path)
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}

	client, err := t.lspMgr.ClientForFile(ctx, abs)
	if err != nil {
		return "", err
	}

	diags, received, err := client.Diagnostics(ctx, abs, diagnosticsWaitTimeout)
	if err != nil {
		return "", err
	}
	if !received {
		return fmt.Sprintf("No diagnostics published for %s within %s (the server may still be indexing).", path, diagnosticsWaitTimeout), nil
	}
	if len(diags) == 0 {
		return fmt.Sprintf("No problems found in %s.", path), nil
	}
	return formatDiagnostics(map[string][]lsp.Diagnostic{abs: diags}, t.shellMgr.WorkingDir()), nil
}

func (t *Toolbox) diagnosticsForProject(ctx context.Context) (string, error) {
	exts := map[string]bool{}
	for _, e := range t.lspMgr.Extensions() {
		exts[strings.ToLower(e)] = true
	}
	if len(exts) == 0 {
		return "", fmt.Errorf("no language servers are configured")
	}

	root := t.shellMgr.WorkingDir()
	var files []string
	walkFiles(root, func(p string, _ fs.DirEntry) bool {
		if hasExt(p, exts) {
			files = append(files, p)
		}
		return len(files) < maxProjectDiagFiles
	})

	if len(files) == 0 {
		return "No files matching a configured language server were found.", nil
	}

	// Open each file so the server analyzes it.
	clients := map[string]*lsp.Client{}
	for _, f := range files {
		client, err := t.lspMgr.ClientForFile(ctx, f)
		if err != nil {
			continue
		}
		_, _, _ = client.Diagnostics(ctx, f, 50*time.Millisecond)
		clients[clientKey(client)] = client
	}

	// Give servers a moment to finish publishing.
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(diagnosticsWaitTimeout):
	}

	all := map[string][]lsp.Diagnostic{}
	for _, client := range clients {
		for uri, diags := range client.AllDiagnostics() {
			if len(diags) > 0 {
				all[lsp.URIToPath(uri)] = diags
			}
		}
	}
	if len(all) == 0 {
		return fmt.Sprintf("No problems found across %d file(s).", len(files)), nil
	}
	return formatDiagnostics(all, root), nil
}

// LspReferences finds all references to a symbol using grep to locate candidate
// positions and the language server's textDocument/references to resolve them.
func (t *Toolbox) LspReferences(ctx context.Context, params LspReferencesParams) (string, error) {
	if !t.lspMgr.Configured() {
		return "", fmt.Errorf("no language servers are configured")
	}
	if params.Symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}

	searchPath := t.shellMgr.WorkingDir()
	if params.Path != "" {
		searchPath = t.resolvePath(params.Path)
	}

	exts := map[string]bool{}
	for _, e := range t.lspMgr.Extensions() {
		exts[strings.ToLower(e)] = true
	}

	candidates := t.findSymbolCandidates(searchPath, params.Symbol, exts)
	if len(candidates) == 0 {
		return fmt.Sprintf("Symbol %q not found in %s.", params.Symbol, params.Path), nil
	}

	seen := map[string]bool{}
	var locations []lsp.Location
	for _, cand := range candidates {
		client, err := t.lspMgr.ClientForFile(ctx, cand.path)
		if err != nil {
			continue
		}
		locs, err := client.References(ctx, cand.path, lsp.Position{Line: cand.line, Character: cand.col})
		if err != nil || len(locs) == 0 {
			continue
		}
		for _, l := range locs {
			key := fmt.Sprintf("%s:%d:%d", l.URI, l.Range.Start.Line, l.Range.Start.Character)
			if seen[key] {
				continue
			}
			seen[key] = true
			locations = append(locations, l)
		}
		if len(locations) > 0 {
			break
		}
	}

	if len(locations) == 0 {
		return fmt.Sprintf("No references found for symbol %q.", params.Symbol), nil
	}
	return formatReferences(params.Symbol, locations, t.shellMgr.WorkingDir()), nil
}

// LspRestart restarts a named language server, or all of them when name is empty.
func (t *Toolbox) LspRestart(_ context.Context, params LspRestartParams) (string, error) {
	if !t.lspMgr.Configured() {
		return "", fmt.Errorf("no language servers are configured")
	}
	restarted, err := t.lspMgr.Restart(params.Name)
	if err != nil {
		return "", err
	}
	if len(restarted) == 0 {
		if params.Name != "" {
			return fmt.Sprintf("Language server %q was not running; it will start on next use.", params.Name), nil
		}
		return "No language servers were running; they will start on next use.", nil
	}
	return fmt.Sprintf("Restarted language server(s): %s. They will reinitialize on next use.", strings.Join(restarted, ", ")), nil
}

type symbolCandidate struct {
	path string
	line int // zero-based
	col  int // zero-based
}

func (t *Toolbox) findSymbolCandidates(searchPath, symbol string, exts map[string]bool) []symbolCandidate {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return nil
	}

	var candidates []symbolCandidate

	info, err := os.Stat(searchPath)
	if err != nil {
		return nil
	}

	scanFile := func(p string) {
		if len(candidates) >= maxReferenceCandidates {
			return
		}
		if !hasExt(p, exts) {
			return
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return
		}
		for i, line := range strings.Split(string(data), "\n") {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			candidates = append(candidates, symbolCandidate{path: p, line: i, col: loc[0]})
			if len(candidates) >= maxReferenceCandidates {
				return
			}
		}
	}

	if !info.IsDir() {
		scanFile(searchPath)
		return candidates
	}

	walkFiles(searchPath, func(p string, _ fs.DirEntry) bool {
		scanFile(p)
		return len(candidates) < maxReferenceCandidates
	})
	return candidates
}

func clientKey(c *lsp.Client) string {
	return fmt.Sprintf("%p", c)
}

func formatDiagnostics(byFile map[string][]lsp.Diagnostic, root string) string {
	paths := make([]string, 0, len(byFile))
	for p := range byFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var buf strings.Builder
	total := 0
	for _, p := range paths {
		diags := byFile[p]
		if len(diags) == 0 {
			continue
		}
		sort.Slice(diags, func(i, j int) bool {
			if diags[i].Range.Start.Line != diags[j].Range.Start.Line {
				return diags[i].Range.Start.Line < diags[j].Range.Start.Line
			}
			return diags[i].Range.Start.Character < diags[j].Range.Start.Character
		})
		fmt.Fprintf(&buf, "%s\n", relTo(root, p))
		for _, d := range diags {
			total++
			src := d.Source
			if src != "" {
				src = " (" + src + ")"
			}
			fmt.Fprintf(&buf, "  %d:%d %s: %s%s\n",
				d.Range.Start.Line+1, d.Range.Start.Character+1,
				d.SeverityString(), strings.TrimSpace(d.Message), src)
		}
	}
	fmt.Fprintf(&buf, "\n[%d diagnostic(s) across %d file(s)]", total, len(paths))
	return buf.String()
}

func formatReferences(symbol string, locs []lsp.Location, root string) string {
	sort.Slice(locs, func(i, j int) bool {
		if locs[i].URI != locs[j].URI {
			return locs[i].URI < locs[j].URI
		}
		return locs[i].Range.Start.Line < locs[j].Range.Start.Line
	})

	var buf strings.Builder
	fmt.Fprintf(&buf, "References to %q:\n", symbol)
	for _, l := range locs {
		p := relTo(root, lsp.URIToPath(l.URI))
		fmt.Fprintf(&buf, "  %s:%d:%d\n", p, l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	fmt.Fprintf(&buf, "\n[%d reference(s)]", len(locs))
	return buf.String()
}

func relTo(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
