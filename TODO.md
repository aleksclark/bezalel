# TODO: Crush Tool Parity

Tools required to reach parity with Crush's filesystem/process-dependent tools.

## Currently Implemented

- [x] `bash` — Shell execution with auto-background promotion
- [x] `job_output` — Poll background job stdout/stderr
- [x] `job_kill` — Terminate a background job
- [x] `view` — Read files with line-based pagination
- [x] `write` — Create/overwrite files (auto-mkdir)
- [x] `edit` — Find-and-replace in files
- [x] `delete` — Remove files/directories
- [x] `ls` — Directory tree listing
- [x] `glob` — Find files by glob pattern (ripgrep + fallback)
- [x] `grep` — Search file contents (ripgrep + fallback)

## Not Yet Implemented

### High Priority (core coding workflow)

- [ ] `multiedit` — Batch multiple find-and-replace operations on a single file
  - Crush exposes this as a separate tool so the LLM can make several edits atomically
  - Params: `file_path`, `edits: [{old_string, new_string, replace_all}]`
  - Edits applied sequentially; partial failure reports which edits succeeded/failed
  - Returns unified diff of the combined changes
  - Reference: `crush/internal/agent/tools/multiedit.go`

- [ ] `download` — Download a URL to a local file
  - HTTP GET with streaming write to disk
  - Params: `url`, `file_path`, `timeout` (optional, max 600s)
  - Creates parent directories automatically
  - Returns file size and path on success
  - Reference: `crush/internal/agent/tools/download.go`

### Medium Priority (network + filesystem hybrid)

- [ ] `fetch` — Fetch URL content and return as text/markdown/html
  - HTTP GET, converts HTML to markdown via goquery + html-to-markdown
  - Params: `url`, `format` (text|markdown|html)
  - Truncates large responses; optionally saves to temp file when oversized
  - Separate from `download` (fetch returns content inline, download saves to disk)
  - Reference: `crush/internal/agent/tools/fetch.go`

- [ ] `web_fetch` — Simplified fetch for sub-agents (no permissions)
  - Same as fetch but skips permission checks
  - Saves large content to temp file in working directory, returns path
  - Reference: `crush/internal/agent/tools/web_fetch.go`

### Lower Priority (LSP integration)

- [ ] `lsp_diagnostics` — Get compiler/linter diagnostics from language servers
  - Requires running LSP sub-processes inside the pod
  - Params: `file_path` (optional — empty = project-wide)
  - Returns structured diagnostic messages (errors, warnings) with locations
  - Significant complexity: LSP lifecycle management, multi-language support
  - Reference: `crush/internal/agent/tools/diagnostics.go`

- [ ] `lsp_references` — Find all references to a symbol via LSP
  - Requires active LSP sessions with workspace indexing
  - Params: `symbol`, `path` (optional scope)
  - Uses grep to find candidate files, then LSP textDocument/references
  - Reference: `crush/internal/agent/tools/references.go`

### Not Applicable (handled outside the pod)

These Crush tools do NOT need bezalel implementations because they don't depend
on local filesystem or process execution:

- `web_search` — Pure HTTP to DuckDuckGo (agent loop provides this)
- `sourcegraph` — Pure HTTP to Sourcegraph API (agent loop provides this)
- `todos` — Session state management (lives in orchestrator, not pod)
- `agent` — Sub-agent spawning (orchestrator concern)

### Supporting Infrastructure

- [ ] Configurable command blocklist (Crush's `bannedCommands` + `BlockFunc`)
- [ ] Safe command detection (read-only commands skip permission checks)
- [ ] Output truncation metadata (return truncation info alongside content)
- [ ] LSP server lifecycle manager (start/stop/restart language servers)
- [ ] File change tracking / history (undo support for edit/write/delete)

## Architecture Notes

Crush uses `mvdan/sh` (in-process POSIX shell interpreter) for cross-platform
compatibility. Bezalel uses real `sh -c` since it always runs in a Linux
container. This means:

- No Windows compatibility needed
- Shell builtins and pipes work natively
- Process signals (SIGTERM/SIGKILL) work correctly for job_kill
- Environment variable isolation is handled by the container, not the interpreter

For LSP tools, the key decision is whether to:
1. Bundle language servers in the container image (larger image, zero-config)
2. Let the agent install them at runtime (smaller image, first-run cost)
3. Make them optional sidecar containers (composable, k8s-native)

Option 3 is most aligned with the k8s operator model — the operator's CRD
could specify which language servers to attach based on the project's languages.
