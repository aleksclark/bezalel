# Bezalel

> *"Then the LORD said to Moses, 'See, I have chosen Bezalel... and I have filled him with the Spirit of God, with wisdom, with understanding, with knowledge and with all kinds of skills— to make artistic designs for work in gold, silver and bronze, to cut and set stones, to work in wood, and to engage in all kinds of crafts.'"* — Exodus 31:1-5

An MCP (Model Context Protocol) server sidecar that provides AI agent execution environments with shell, filesystem, and development tooling.

## Overview

Bezalel runs as a sidecar container in Kubernetes pods, exposing a complete development environment over MCP's Streamable HTTP transport. It is designed for AI agent loops where each iteration may execute on a different machine, but needs consistent access to:

- **Shell execution** with background job management (foreground with auto-background promotion)
- **Filesystem operations** (read, write, edit, multiedit, delete, list, glob, grep)
- **Process lifecycle** (start, poll, kill background jobs)
- **Network fetch** (download to disk, fetch URL content as text/markdown/html)
- **LSP integration** (diagnostics, references, and lifecycle management for pod-installed language servers)

## Design Principles

- **Crush-compatible semantics**: Tool behavior matches [Crush](https://github.com/charmbracelet/crush)'s bash tool — synchronous execution with automatic background promotion after a configurable threshold, output truncation, exit code reporting.
- **Stateless between calls**: Each shell invocation is independent. The pod provides persistence (working directory, installed tools), not the shell session.
- **Single static binary**: Compiles to one Go binary suitable for a minimal container image.
- **Streamable HTTP transport**: Network-accessible MCP over HTTP, not stdio. Multiple clients can connect.

## Tools

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands with auto-background after timeout |
| `job_output` | Get stdout/stderr from a background job |
| `job_kill` | Terminate a background job |
| `view` | Read file contents with line-based pagination |
| `write` | Create or overwrite files |
| `edit` | Find-and-replace in files |
| `delete` | Remove files or directories |
| `ls` | List directory tree |
| `glob` | Find files by glob pattern (uses ripgrep when available) |
| `grep` | Search file contents (uses ripgrep when available) |
| `multiedit` | Apply multiple find-and-replace edits to a file atomically |
| `download` | Download a URL to a local file (streaming) |
| `fetch` | Fetch a URL and return its content inline (text/markdown/html) |
| `web_fetch` | Fetch a URL, spilling oversized content to a temp file |
| `lsp_diagnostics` | Compiler/linter diagnostics from configured language servers |
| `lsp_references` | Find references to a symbol via a language server |
| `lsp_restart` | Restart a language server (or all of them) |

## Configuration

All settings can be supplied as a CLI flag, an environment variable (prefix `BEZALEL_`),
or a config file (`bezalel.yaml`/`.json`/`.toml` in `.`, `$HOME/.config/bezalel/`, or
`/etc/bezalel/`). Precedence: CLI flag > environment variable > config file > default.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--host` | `BEZALEL_HOST` | (all interfaces) | Host/interface to bind |
| `--port` | `BEZALEL_PORT` | `8080` | Port to listen on |
| `--workdir` | `BEZALEL_WORKDIR` | current directory | Working directory for tool execution |
| `--auth-token` | `BEZALEL_AUTH_TOKEN` | (none) | Bearer token required on `/mcp` requests |
| `--config` | — | (auto-discovered) | Explicit config file path |

When `--auth-token`/`BEZALEL_AUTH_TOKEN` is set, every `/mcp` request must include an
`Authorization: Bearer <token>` header. If no token is configured the server logs a
warning on startup and `/mcp` is publicly accessible.

### Language servers

The `lsp_*` tools require language servers to be installed in the pod (bundled in
the image or added by the operator). Bezalel manages their lifecycle: servers are
started lazily on first use, reused across requests, and shut down on exit.
Declare them under the `lsp` key in the config file:

```yaml
lsp:
  - name: gopls
    command: gopls
    args: []
    extensions: [".go"]
    root_markers: ["go.mod", "go.work"]
    language_id: go
  - name: pyright
    command: pyright-langserver
    args: ["--stdio"]
    extensions: [".py", ".pyi"]
    language_id: python
```

`lsp_restart` stops a server (or all of them when `name` is omitted) so it
reinitializes on next use — handy after changing toolchains or when a server
gets wedged.

## Releases

Releases are cut by manually dispatching the **Release** workflow
(`.github/workflows/release.yml`). It builds static `bezalel` binaries for
`linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`, attaches them
(as `bezalel_<version>_<os>_<arch>.tar.gz` plus a `checksums.txt`) to a GitHub
release, and tags the commit.

Versioning is **CalVer** in the form `YYYYMM.DD.patch` (e.g. `202606.24.0`),
which is also a valid SemVer `major.minor.patch` triple. The patch component
auto-increments for multiple releases on the same UTC day. The version is
injected into the binary at build time via `-ldflags` and is reported by
`bezalel --version`.

## Status

🚧 Initial spike.

## License

MIT
