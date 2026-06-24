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
- [x] `multiedit` — Batch find-and-replace operations on a single file (atomic)
- [x] `download` — Download a URL to a local file (streaming)
- [x] `fetch` — Fetch URL content inline as text/markdown/html
- [x] `web_fetch` — Fetch URL content, spilling oversized content to a temp file

## Not Yet Implemented

### Lower Priority (LSP integration)

> Deferred: the LSP tools depend on running language servers inside the pod, and
> the bundling strategy (see "Architecture Notes" below) is still an open product
> decision. They are intentionally left unimplemented until that is resolved so we
> don't bake a language-server lifecycle manager into the image prematurely.

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
