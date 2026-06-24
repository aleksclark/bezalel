// Package tools - filesystem tool implementations for bezalel MCP server.
package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxReadSize    = 5 * 1024 * 1024 // 5MB
	maxLineLength  = 2000
	defaultLimit   = 2000
	maxLsEntries   = 1000
	maxGlobResults = 100
	maxGrepResults = 50
	defaultDepth   = 3
)

// ViewParams are the parameters for the view tool.
type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// WriteParams are the parameters for the write tool.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// EditParams are the parameters for the edit tool.
type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// DeleteParams are the parameters for the delete tool.
type DeleteParams struct {
	FilePath  string `json:"file_path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// LsParams are the parameters for the ls tool.
type LsParams struct {
	Path   string   `json:"path,omitempty"`
	Ignore []string `json:"ignore,omitempty"`
	Depth  int      `json:"depth,omitempty"`
}

// GlobParams are the parameters for the glob tool.
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// GrepParams are the parameters for the grep tool.
type GrepParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"`
}

// View reads file contents with line-based pagination.
func (t *Toolbox) View(params ViewParams) (string, error) {
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	filePath := t.resolvePath(params.FilePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", params.FilePath)
	}
	if info.Size() > maxReadSize {
		return "", fmt.Errorf("file is too large (%d bytes, max %d bytes)", info.Size(), maxReadSize)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}

	// Binary detection: if >20% non-UTF8 bytes, reject
	if isBinary(data) {
		return "", fmt.Errorf("file appears to be binary (contains non-UTF8 data)")
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	limit := params.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	offset := params.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= totalLines {
		return fmt.Sprintf("(empty — offset %d is beyond end of file with %d lines)", offset, totalLines), nil
	}

	end := offset + limit
	if end > totalLines {
		end = totalLines
	}

	var buf strings.Builder
	lineNumWidth := len(fmt.Sprintf("%d", end))
	for i := offset; i < end; i++ {
		line := lines[i]
		if len(line) > maxLineLength {
			line = line[:maxLineLength] + "..."
		}
		buf.WriteString(fmt.Sprintf("%*d|%s\n", lineNumWidth, i+1, line))
	}

	buf.WriteString(fmt.Sprintf("\n[Total lines: %d | Showing: %d-%d]", totalLines, offset+1, end))

	return buf.String(), nil
}

// Write creates or overwrites a file.
func (t *Toolbox) Write(params WriteParams) (string, error) {
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	filePath := t.resolvePath(params.FilePath)

	// Read existing content for diff summary
	var oldContent string
	var existed bool
	if data, err := os.ReadFile(filePath); err == nil {
		oldContent = string(data)
		existed = true
	}

	// Create parent directories
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create directory %s: %w", dir, err)
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(params.Content), 0644); err != nil {
		return "", fmt.Errorf("cannot write file: %w", err)
	}

	// Generate diff summary
	if !existed {
		newLines := strings.Count(params.Content, "\n") + 1
		if params.Content == "" {
			newLines = 0
		}
		return fmt.Sprintf("Created %s (%d lines)", params.FilePath, newLines), nil
	}

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(params.Content, "\n")
	added, removed := diffSummary(oldLines, newLines)
	return fmt.Sprintf("Updated %s: %d lines added, %d lines removed (was %d lines, now %d lines)",
		params.FilePath, added, removed, len(oldLines), len(newLines)), nil
}

// Edit performs find-and-replace in a file.
func (t *Toolbox) Edit(params EditParams) (string, error) {
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if params.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	filePath := t.resolvePath(params.FilePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}

	content := string(data)
	count := strings.Count(content, params.OldString)

	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if count > 1 && !params.ReplaceAll {
		return "", fmt.Errorf("old_string has %d matches; set replace_all=true to replace all, or provide a more unique string", count)
	}

	var newContent string
	if params.ReplaceAll {
		newContent = strings.ReplaceAll(content, params.OldString, params.NewString)
	} else {
		newContent = strings.Replace(content, params.OldString, params.NewString, 1)
	}

	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("cannot write file: %w", err)
	}

	// Generate unified diff
	diff := generateUnifiedDiff(params.FilePath, content, newContent)
	replacements := 1
	if params.ReplaceAll {
		replacements = count
	}
	return fmt.Sprintf("%s\n(%d replacement(s) made)", diff, replacements), nil
}

// Delete removes a file or directory.
func (t *Toolbox) Delete(params DeleteParams) (string, error) {
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	filePath := t.resolvePath(params.FilePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path: %w", err)
	}

	if info.IsDir() {
		// Check if directory is non-empty
		entries, err := os.ReadDir(filePath)
		if err != nil {
			return "", fmt.Errorf("cannot read directory: %w", err)
		}
		if len(entries) > 0 && !params.Recursive {
			return "", fmt.Errorf("directory is not empty; set recursive=true to remove")
		}
		if err := os.RemoveAll(filePath); err != nil {
			return "", fmt.Errorf("cannot remove directory: %w", err)
		}
		return fmt.Sprintf("Deleted directory: %s", params.FilePath), nil
	}

	if err := os.Remove(filePath); err != nil {
		return "", fmt.Errorf("cannot remove file: %w", err)
	}
	return fmt.Sprintf("Deleted file: %s", params.FilePath), nil
}

// Ls lists directory contents in a tree-style format.
func (t *Toolbox) Ls(params LsParams) (string, error) {
	rootPath := params.Path
	if rootPath == "" {
		rootPath = t.shellMgr.WorkingDir()
	} else {
		rootPath = t.resolvePath(rootPath)
	}

	depth := params.Depth
	if depth <= 0 {
		depth = defaultDepth
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		return "", fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", params.Path)
	}

	var buf strings.Builder
	count := 0
	truncated := false

	var walk func(dir string, prefix string, currentDepth int)
	walk = func(dir string, prefix string, currentDepth int) {
		if truncated || currentDepth > depth {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		// Filter entries based on ignore patterns
		filtered := make([]os.DirEntry, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if shouldIgnore(name, params.Ignore) {
				continue
			}
			filtered = append(filtered, e)
		}

		for i, entry := range filtered {
			if count >= maxLsEntries {
				truncated = true
				return
			}

			isLast := i == len(filtered)-1
			connector := "├── "
			childPrefix := prefix + "│   "
			if isLast {
				connector = "└── "
				childPrefix = prefix + "    "
			}

			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			buf.WriteString(prefix + connector + name + "\n")
			count++

			if entry.IsDir() && currentDepth < depth {
				walk(filepath.Join(dir, entry.Name()), childPrefix, currentDepth+1)
			}
		}
	}

	buf.WriteString(rootPath + "/\n")
	count++
	walk(rootPath, "", 1)

	if truncated {
		buf.WriteString(fmt.Sprintf("\n... (truncated at %d entries)\n", maxLsEntries))
	}

	return buf.String(), nil
}

// Glob finds files matching a glob pattern.
func (t *Toolbox) Glob(params GlobParams) (string, error) {
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = t.shellMgr.WorkingDir()
	} else {
		searchPath = t.resolvePath(searchPath)
	}

	// Try ripgrep first
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return t.globWithRipgrep(rgPath, params.Pattern, searchPath)
	}

	// Fallback to Go filepath.WalkDir with pattern matching
	return t.globWithGo(params.Pattern, searchPath)
}

// Grep searches file contents by regex pattern.
func (t *Toolbox) Grep(params GrepParams) (string, error) {
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = t.shellMgr.WorkingDir()
	} else {
		searchPath = t.resolvePath(searchPath)
	}

	// Try ripgrep first
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return t.grepWithRipgrep(rgPath, params.Pattern, searchPath, params.Include)
	}

	// Fallback to Go implementation
	return t.grepWithGo(params.Pattern, searchPath, params.Include)
}

// --- Helper functions ---

func (t *Toolbox) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(t.shellMgr.WorkingDir(), p)
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// Check first 8KB for binary content
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	nonUTF8 := 0
	total := len(sample)
	for i := 0; i < len(sample); {
		r, size := utf8.DecodeRune(sample[i:])
		if r == utf8.RuneError && size == 1 {
			// Check for null bytes and other control characters
			if sample[i] == 0 {
				nonUTF8++
			} else if sample[i] < 0x20 && sample[i] != '\n' && sample[i] != '\r' && sample[i] != '\t' {
				nonUTF8++
			} else {
				nonUTF8++
			}
		}
		i += size
	}
	return float64(nonUTF8)/float64(total) > 0.20
}

func diffSummary(oldLines, newLines []string) (added, removed int) {
	// Simple line-count based diff summary
	oldSet := make(map[string]int)
	for _, l := range oldLines {
		oldSet[l]++
	}
	newSet := make(map[string]int)
	for _, l := range newLines {
		newSet[l]++
	}

	for _, l := range newLines {
		if oldSet[l] > 0 {
			oldSet[l]--
		} else {
			added++
		}
	}
	for _, l := range oldLines {
		if newSet[l] > 0 {
			newSet[l]--
		} else {
			removed++
		}
	}
	return added, removed
}

func generateUnifiedDiff(filename, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("--- a/%s\n", filename))
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filename))

	// Find changed regions and output context
	const contextLines = 3
	type change struct {
		oldStart, oldEnd int
		newStart, newEnd int
	}

	// Simple diff: find first and last differing lines
	var changes []change
	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		// Skip matching lines
		if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			i++
			j++
			continue
		}
		// Found a difference - find extent
		startI, startJ := i, j
		// Advance through the diff
		for i < len(oldLines) || j < len(newLines) {
			if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
				// Check if we have enough matching context to end this hunk
				matchCount := 0
				for k := 0; i+k < len(oldLines) && j+k < len(newLines) && oldLines[i+k] == newLines[j+k]; k++ {
					matchCount++
					if matchCount >= 2*contextLines {
						break
					}
				}
				if matchCount >= 2*contextLines {
					break
				}
				i++
				j++
			} else if i < len(oldLines) && (j >= len(newLines) || !containsAt(newLines, j, oldLines[i])) {
				i++
			} else {
				j++
			}
		}
		changes = append(changes, change{startI, i, startJ, j})
	}

	for _, c := range changes {
		// Context before
		ctxStart := c.oldStart - contextLines
		if ctxStart < 0 {
			ctxStart = 0
		}
		ctxEnd := c.oldEnd + contextLines
		if ctxEnd > len(oldLines) {
			ctxEnd = len(oldLines)
		}
		newCtxStart := c.newStart - contextLines
		if newCtxStart < 0 {
			newCtxStart = 0
		}
		newCtxEnd := c.newEnd + contextLines
		if newCtxEnd > len(newLines) {
			newCtxEnd = len(newLines)
		}

		buf.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n",
			ctxStart+1, ctxEnd-ctxStart,
			newCtxStart+1, newCtxEnd-newCtxStart))

		// Context before
		for k := ctxStart; k < c.oldStart; k++ {
			buf.WriteString(" " + oldLines[k] + "\n")
		}
		// Removed lines
		for k := c.oldStart; k < c.oldEnd; k++ {
			buf.WriteString("-" + oldLines[k] + "\n")
		}
		// Added lines
		for k := c.newStart; k < c.newEnd; k++ {
			buf.WriteString("+" + newLines[k] + "\n")
		}
		// Context after
		for k := c.oldEnd; k < ctxEnd; k++ {
			buf.WriteString(" " + oldLines[k] + "\n")
		}
	}

	return buf.String()
}

func containsAt(lines []string, start int, target string) bool {
	// Simple lookahead to see if target appears within a few lines
	for i := start; i < start+3 && i < len(lines); i++ {
		if lines[i] == target {
			return true
		}
	}
	return false
}

func shouldIgnore(name string, patterns []string) bool {
	// Always ignore common hidden/build dirs
	defaults := []string{".git", "node_modules", "__pycache__", ".DS_Store"}
	for _, d := range defaults {
		if name == d {
			return true
		}
	}
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

func (t *Toolbox) globWithRipgrep(rgPath, pattern, searchPath string) (string, error) {
	cmd := exec.Command(rgPath, "--files", "--glob", pattern, searchPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // rg returns exit code 1 when no matches

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "No matches found.", nil
	}

	if len(lines) > maxGlobResults {
		lines = lines[:maxGlobResults]
		return strings.Join(lines, "\n") + fmt.Sprintf("\n\n... (truncated at %d results)", maxGlobResults), nil
	}

	return strings.Join(lines, "\n"), nil
}

func (t *Toolbox) globWithGo(pattern, searchPath string) (string, error) {
	var results []string

	err := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if len(results) >= maxGlobResults {
			return filepath.SkipAll
		}

		name := d.Name()
		// Skip hidden directories
		if d.IsDir() && strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		if d.IsDir() && (name == "node_modules" || name == "__pycache__") {
			return filepath.SkipDir
		}

		if !d.IsDir() {
			matched, _ := filepath.Match(pattern, name)
			if matched {
				results = append(results, path)
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob walk error: %w", err)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	output := strings.Join(results, "\n")
	if len(results) >= maxGlobResults {
		output += fmt.Sprintf("\n\n... (truncated at %d results)", maxGlobResults)
	}
	return output, nil
}

func (t *Toolbox) grepWithRipgrep(rgPath, pattern, searchPath, include string) (string, error) {
	args := []string{"--line-number", "--no-heading", "--max-count", "50", pattern, searchPath}
	if include != "" {
		args = []string{"--line-number", "--no-heading", "--max-count", "50", "--glob", include, pattern, searchPath}
	}

	cmd := exec.Command(rgPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // rg returns exit code 1 when no matches

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return "No matches found.", nil
	}

	lines := strings.Split(output, "\n")
	if len(lines) > maxGrepResults {
		lines = lines[:maxGrepResults]
		return strings.Join(lines, "\n") + fmt.Sprintf("\n\n... (truncated at %d results)", maxGrepResults), nil
	}

	return output, nil
}

func (t *Toolbox) grepWithGo(pattern, searchPath, include string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	var results []string

	walkErr := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(results) >= maxGrepResults {
			return filepath.SkipAll
		}

		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check include filter
		if include != "" {
			matched, _ := filepath.Match(include, name)
			if !matched {
				return nil
			}
		}

		// Skip binary/large files
		info, err := d.Info()
		if err != nil || info.Size() > maxReadSize {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d:%s", path, lineNum, line))
				if len(results) >= maxGrepResults {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("grep walk error: %w", walkErr)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	output := strings.Join(results, "\n")
	if len(results) >= maxGrepResults {
		output += fmt.Sprintf("\n\n... (truncated at %d results)", maxGrepResults)
	}
	return output, nil
}
