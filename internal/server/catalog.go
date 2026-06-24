package server

import "github.com/aleksclark/bezalel/internal/tools"

// buildRegistry registers every bezalel tool: its name, description, input
// schema, and handler. This is the single place to edit when adding or changing
// a tool — bind() wires the toolbox method to the JSON-RPC layer.
func buildRegistry(tb *tools.Toolbox) *registry {
	r := newRegistry()

	r.add("bash",
		"Execute a shell command. Commands taking longer than 1 minute are automatically moved to background.",
		object(map[string]any{
			"command":           prop("string", "The command to execute"),
			"description":       prop("string", "Brief description of what the command does"),
			"working_dir":       prop("string", "Working directory (defaults to server working directory)"),
			"run_in_background": prop("boolean", "Run immediately as a background job"),
		}, "command"),
		bind(tb.ExecBash))

	r.add("job_output",
		"Get the current output of a background job.",
		object(map[string]any{
			"job_id": prop("string", "The ID of the background job"),
		}, "job_id"),
		bind(tb.GetJobOutput))

	r.add("job_kill",
		"Terminate a background job.",
		object(map[string]any{
			"job_id": prop("string", "The ID of the background job to terminate"),
		}, "job_id"),
		bind(tb.KillJob))

	r.add("view",
		"Read file contents with line-based pagination. Returns numbered lines. Max file size 5MB.",
		object(map[string]any{
			"file_path": prop("string", "Path to the file to read"),
			"offset":    prop("integer", "0-based line number to start reading from (default: 0)"),
			"limit":     prop("integer", "Maximum number of lines to read (default: 2000)"),
		}, "file_path"),
		bind(tb.View))

	r.add("write",
		"Create or overwrite a file. Creates parent directories automatically.",
		object(map[string]any{
			"file_path": prop("string", "Path to the file to write"),
			"content":   prop("string", "Content to write to the file"),
		}, "file_path", "content"),
		bind(tb.Write))

	r.add("edit",
		"Find-and-replace in a file. old_string must be unique unless replace_all is true. Returns a unified diff.",
		object(map[string]any{
			"file_path":   prop("string", "Path to the file to edit"),
			"old_string":  prop("string", "Text to find in the file (must be unique unless replace_all=true)"),
			"new_string":  prop("string", "Replacement text"),
			"replace_all": prop("boolean", "Replace all occurrences instead of requiring uniqueness (default: false)"),
		}, "file_path", "old_string", "new_string"),
		bind(tb.Edit))

	r.add("delete",
		"Remove a file or directory. Non-empty directories require recursive=true.",
		object(map[string]any{
			"file_path": prop("string", "Path to the file or directory to delete"),
			"recursive": prop("boolean", "Recursively delete non-empty directories (default: false)"),
		}, "file_path"),
		bind(tb.Delete))

	r.add("ls",
		"List directory contents in a tree-style format. Respects .gitignore-like patterns. Max 1000 entries.",
		object(map[string]any{
			"path":   prop("string", "Directory to list (defaults to working directory)"),
			"ignore": arrayProp("Glob patterns to ignore", map[string]any{"type": "string"}),
			"depth":  prop("integer", "Maximum directory depth to traverse (default: 3)"),
		}),
		bind(tb.Ls))

	r.add("glob",
		"Find files matching a glob pattern. Uses ripgrep if available. Max 100 results.",
		object(map[string]any{
			"pattern": prop("string", "Glob pattern to match files (e.g., '*.go', '**/*.json')"),
			"path":    prop("string", "Directory to search in (defaults to working directory)"),
		}, "pattern"),
		bind(tb.Glob))

	r.add("grep",
		"Search file contents by regex pattern. Uses ripgrep if available. Max 50 matches. Returns filepath:line_number:content format.",
		object(map[string]any{
			"pattern": prop("string", "Regex pattern to search for"),
			"path":    prop("string", "Directory or file to search in (defaults to working directory)"),
			"include": prop("string", "Glob filter to restrict which files are searched (e.g., '*.go')"),
		}, "pattern"),
		bind(tb.Grep))

	r.add("multiedit",
		"Apply multiple find-and-replace edits to a single file sequentially in one operation. All edits are applied atomically — if any edit fails, no changes are written. Returns a unified diff of the combined changes.",
		object(map[string]any{
			"file_path": prop("string", "Path to the file to edit"),
			"edits": arrayProp("Edits applied in order. Each must have old_string and new_string; set replace_all for multiple occurrences.",
				object(map[string]any{
					"old_string":  prop("string", "Text to find (must be unique unless replace_all=true)"),
					"new_string":  prop("string", "Replacement text"),
					"replace_all": prop("boolean", "Replace all occurrences instead of requiring uniqueness (default: false)"),
				}, "old_string", "new_string")),
		}, "file_path", "edits"),
		bind(tb.MultiEdit))

	r.add("download",
		"Download a URL to a local file. Streams the response to disk, creating parent directories automatically. Returns the byte count on success.",
		object(map[string]any{
			"url":       prop("string", "HTTP(S) URL to download"),
			"file_path": prop("string", "Local path to save the downloaded content"),
			"timeout":   prop("integer", "Timeout in seconds (default 120, max 600)"),
		}, "url", "file_path"),
		bind(tb.Download))

	r.add("fetch",
		"Fetch a URL and return its content inline. HTML is converted to the requested format. Large responses are truncated.",
		object(map[string]any{
			"url":     prop("string", "HTTP(S) URL to fetch"),
			"format":  enumProp("Output format: text, markdown (default), or html", "text", "markdown", "html"),
			"timeout": prop("integer", "Timeout in seconds (default 120, max 600)"),
		}, "url"),
		bind(tb.Fetch))

	r.add("web_fetch",
		"Fetch a URL and return its content. Behaves like fetch but spills oversized content to a temp file in the working directory and returns the path.",
		object(map[string]any{
			"url":     prop("string", "HTTP(S) URL to fetch"),
			"format":  enumProp("Output format: text, markdown (default), or html", "text", "markdown", "html"),
			"timeout": prop("integer", "Timeout in seconds (default 120, max 600)"),
		}, "url"),
		bind(tb.WebFetch))

	r.add("lsp_diagnostics",
		"Get compiler/linter diagnostics from configured language servers. Provide file_path for a single file, or omit it for project-wide diagnostics.",
		object(map[string]any{
			"file_path": prop("string", "File to diagnose (omit for project-wide diagnostics)"),
		}),
		bind(tb.LspDiagnostics))

	r.add("lsp_references",
		"Find all references to a symbol via a language server. Uses grep to locate candidate positions, then resolves them with textDocument/references.",
		object(map[string]any{
			"symbol": prop("string", "The symbol name to find references for"),
			"path":   prop("string", "Directory or file to scope the search (defaults to working directory)"),
		}, "symbol"),
		bind(tb.LspReferences))

	r.add("lsp_restart",
		"Restart a language server (or all of them when name is omitted). Servers reinitialize lazily on next use.",
		object(map[string]any{
			"name": prop("string", "Name of the language server to restart (omit to restart all)"),
		}),
		bind(tb.LspRestart))

	return r
}
