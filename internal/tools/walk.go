// Package tools - shared filesystem traversal helpers.
package tools

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// ignoredDirs are well-known vendor/build directory names skipped by all
// recursive tools (glob, grep, and the LSP scans).
var ignoredDirs = map[string]bool{
	"node_modules": true,
	"__pycache__":  true,
}

// skipDirName reports whether a directory with the given name should be skipped
// during recursive traversal: hidden directories (".git", ".venv", ...) and the
// well-known vendor/build directories in ignoredDirs.
//
// Note: this is the policy for recursive *search* tools. The `ls` tool uses its
// own shouldIgnore, which intentionally surfaces most dotfiles.
func skipDirName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	return ignoredDirs[name]
}

// walkFiles walks root and invokes fn for each regular file whose ancestor
// directories are not skipped (see skipDirName). fn returns false to stop the
// walk early (e.g. once a result cap is reached). Per-entry errors are ignored
// so a single unreadable file or directory never aborts the traversal.
func walkFiles(root string, fn func(path string, d fs.DirEntry) bool) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !fn(p, d) {
			return filepath.SkipAll
		}
		return nil
	})
}

// hasExt reports whether path's extension (case-insensitive) is present in the
// set. An empty set matches every file.
func hasExt(path string, exts map[string]bool) bool {
	if len(exts) == 0 {
		return true
	}
	return exts[strings.ToLower(filepath.Ext(path))]
}
