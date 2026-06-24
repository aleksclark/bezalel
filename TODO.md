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
- [x] `lsp_diagnostics` — Compiler/linter diagnostics from configured language servers
- [x] `lsp_references` — Find references to a symbol via LSP
- [x] `lsp_restart` — Restart a language server (or all of them)

## Not Yet Implemented

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
- [x] LSP server lifecycle manager (lazy start, restart, shutdown — `internal/lsp`)
- [ ] File change tracking / history (undo support for edit/write/delete)

## Architecture Notes

Crush uses `mvdan/sh` (in-process POSIX shell interpreter) for cross-platform
compatibility. Bezalel uses real `sh -c` since it always runs in a Linux
container. This means:

- No Windows compatibility needed
- Shell builtins and pipes work natively
- Process signals (SIGTERM/SIGKILL) work correctly for job_kill
- Environment variable isolation is handled by the container, not the interpreter

### LSP integration (implemented)

Language servers are assumed to be **installed in the pod environment** (bundled
in the image or added by the operator's CRD). Bezalel owns their *lifecycle*:
`internal/lsp` lazily starts each configured server on first use, performs the
LSP `initialize` handshake, demuxes JSON-RPC over stdio, collects
`publishDiagnostics`, resolves `textDocument/references`, and shuts servers down
on exit. `lsp_restart` stops a server so it relaunches (reinitializes) on next
use.

Servers are declared in config under the `lsp` key, e.g.:

```yaml
lsp:
  - name: gopls
    command: gopls
    extensions: [".go"]
    root_markers: ["go.mod", "go.work"]
    language_id: go
```

`lsp_references` locates candidate positions with a grep-style scan (restricted
to configured extensions) and then resolves them via `textDocument/references`,
mirroring Crush's approach.

