# Bezalel

> *"Then the LORD said to Moses, 'See, I have chosen Bezalel... and I have filled him with the Spirit of God, with wisdom, with understanding, with knowledge and with all kinds of skills— to make artistic designs for work in gold, silver and bronze, to cut and set stones, to work in wood, and to engage in all kinds of crafts.'"* — Exodus 31:1-5

An MCP (Model Context Protocol) server sidecar that provides AI agent execution environments with shell, filesystem, and development tooling.

## Overview

Bezalel runs as a sidecar container in Kubernetes pods, exposing a complete development environment over MCP's Streamable HTTP transport. It is designed for AI agent loops where each iteration may execute on a different machine, but needs consistent access to:

- **Shell execution** with background job management (foreground with auto-background promotion)
- **Filesystem operations** (read, write, edit, delete, list, glob, grep)
- **Process lifecycle** (start, poll, kill background jobs)

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

## Status

🚧 Initial spike — not yet functional.

## License

MIT
