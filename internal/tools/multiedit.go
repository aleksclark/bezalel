// Package tools - multiedit tool implementation for bezalel MCP server.
package tools

import (
	"fmt"
	"os"
	"strings"
)

// MultiEditOperation is a single find-and-replace operation within a multiedit.
type MultiEditOperation struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// MultiEditParams are the parameters for the multiedit tool.
type MultiEditParams struct {
	FilePath string               `json:"file_path"`
	Edits    []MultiEditOperation `json:"edits"`
}

// MultiEdit applies multiple find-and-replace operations to a single file
// sequentially. All edits are applied in order against the in-memory content
// and written once at the end; if any edit fails, no changes are written.
func (t *Toolbox) MultiEdit(params MultiEditParams) (string, error) {
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if len(params.Edits) == 0 {
		return "", fmt.Errorf("at least one edit is required")
	}

	filePath := t.resolvePath(params.FilePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}

	original := string(data)
	content := original
	totalReplacements := 0

	for i, e := range params.Edits {
		if e.OldString == "" {
			return "", fmt.Errorf("edit %d: old_string is required", i+1)
		}
		if e.OldString == e.NewString {
			return "", fmt.Errorf("edit %d: old_string and new_string are identical", i+1)
		}

		count := strings.Count(content, e.OldString)
		if count == 0 {
			return "", fmt.Errorf("edit %d: old_string not found in file", i+1)
		}
		if count > 1 && !e.ReplaceAll {
			return "", fmt.Errorf("edit %d: old_string has %d matches; set replace_all=true to replace all, or provide a more unique string", i+1, count)
		}

		if e.ReplaceAll {
			content = strings.ReplaceAll(content, e.OldString, e.NewString)
			totalReplacements += count
		} else {
			content = strings.Replace(content, e.OldString, e.NewString, 1)
			totalReplacements++
		}
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("cannot write file: %w", err)
	}

	diff := generateUnifiedDiff(params.FilePath, original, content)
	return fmt.Sprintf("%s\n(%d edit(s) applied, %d replacement(s) made)", diff, len(params.Edits), totalReplacements), nil
}
