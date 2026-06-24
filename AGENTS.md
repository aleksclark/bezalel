# AGENTS.md

Bezalel is an MCP (Model Context Protocol) server sidecar written in Go. It exposes
shell execution, background job management, and filesystem operations to AI agents over
MCP's Streamable HTTP transport (JSON-RPC over `POST /mcp`, not stdio). Tool semantics
intentionally mirror [Crush](https://github.com/charmbracelet/crush)'s bash tool.

Module: `github.com/aleksclark/bezalel` · Go 1.26.3. Dependencies: `spf13/cobra` +
`spf13/viper` (CLI/config) in the main module; `mark3labs/mcp-go` is used **only** by the
e2e suite (behind the `e2e` build tag).

## Commands

There is no Makefile; use Go directly. CI (`.github/workflows/ci.yml`) runs on `master`.

```bash
go build ./...                       # build
go test -race -v -count=1 ./...      # unit suite (CI uses -race; keep tests race-clean)
go test -tags e2e -count=1 ./test/e2e/...   # e2e suite — requires Docker
go vet ./...
gofmt -l .                           # CI fails if this lists ANY file; run `gofmt -w .` to fix
go run ./cmd/bezalel --port 8080 --workdir /path --auth-token secret   # run locally
docker build -t bezalel:ci .
```

Lint is `golangci-lint` (CI pins **v2.12**, `--timeout=5m`). Security scans: `govulncheck`
(advisory only) and `gosec` with `-exclude=G204,G301,G304,G306,G703,G122` — these exclusions
are deliberate for a tool whose entire job is running shell commands and touching the
filesystem. Don't "fix" findings by removing those exclusions.

The e2e suite (`test/e2e/`) is gated by `//go:build e2e`, so plain `go test ./...` skips it.
It is NOT a separate module — `go mod tidy` keeps `mcp-go` in `go.mod` because the tagged
file references it. If you remove the e2e tests, run `go mod tidy` to drop the dep.

## Architecture

Request flow: `cmd/bezalel/main.go` → `internal/cli` → `internal/server` → `internal/tools`
→ `internal/shell`.

- **`cmd/bezalel/main.go`** — Tiny entrypoint; just calls `cli.Execute()`.
- **`internal/cli/root.go`** — Cobra root command + Viper config. Every flag is bound to
  viper via `BindPFlag`, so each setting resolves from CLI flag > env (`BEZALEL_*`, dashes →
  underscores) > config file (`bezalel.yaml/json/toml`) > default. Owns the HTTP server
  lifecycle: graceful shutdown on SIGINT/SIGTERM (10s timeout), then `srv.Shutdown()` kills
  all background jobs. HTTP read/write timeouts are **5 minutes** (long-running commands).
  Warns at startup when no auth token is set.
- **`internal/server/server.go`** — Hand-rolled JSON-RPC 2.0 dispatcher. No MCP SDK on the
  server side. Routes: `POST /mcp` (auth-wrapped) and `GET /health` (unauthenticated). Use
  `server.NewWithOptions(server.Options{...})`; `server.New(workdir)` is a no-auth shortcut.
  Bearer-token auth lives in `withAuth`/`authorized` using `crypto/subtle.ConstantTimeCompare`;
  when `authToken == ""` requests pass through. Handles `initialize`, `tools/list`,
  `tools/call`, `notifications/initialized` (returns 204). MCP protocol version is hardcoded
  `2024-11-05`. **Tool schemas in `handleToolsList` and arg-unmarshalling in `handleToolsCall`
  are maintained by hand** — adding/changing a tool means editing both the schema block and the
  switch case, plus the param struct in `internal/tools`.
- **`internal/tools`** — `Toolbox` wraps a `*shell.Manager`. `bash.go` has shell/job tools;
  `filesystem.go` has view/write/edit/delete/ls/glob/grep. Each tool has a `XxxParams` struct
  with json tags matching the schema. Relative paths are resolved against the manager's working
  dir via `resolvePath` (absolute paths pass through unchanged).
- **`internal/shell/shell.go`** — `Manager` runs commands via `sh -c` and tracks background jobs
  in a `sync.Map`. Job IDs are uppercase hex counters (e.g. `001`, `00A`).

## Key behaviors / gotchas

- **Auto-background promotion**: `Manager.Exec` always starts the command as a background job,
  then waits up to `AutoBackgroundThreshold` (1 min). If it finishes in time it returns
  synchronously and the job is removed; otherwise the job is left running and its ID is returned.
  `run_in_background: true` skips the wait but still sleeps 1s to catch fast failures.
- **Tool errors vs RPC errors**: Tool execution failures are returned as a *successful* JSON-RPC
  result with `isError: true` in the content (`toolResult(msg, true)`), NOT as a JSON-RPC error.
  JSON-RPC errors (`-326xx`) are reserved for protocol-level problems (bad params, parse errors).
  Auth failures return HTTP 401 with a `-32001` JSON-RPC error body.
- Output is truncated to `MaxOutputLength` (30000 bytes), preserving head and tail with a
  `... [N lines truncated] ...` marker. `view` rejects files >5MB and binary files (>UTF-8 check
  on first 8KB). Long lines in `view` are clipped to 2000 chars.
- Limits: 50 concurrent background jobs, completed jobs retained 8h then cleaned on next `bash`
  call. glob max 100 results, grep max 50 matches.
- `glob`/`grep` prefer `ripgrep` (`rg`) when on PATH, with stdlib fallback. The Docker image
  installs `bash coreutils git ripgrep` into `alpine:3.20`; the binary is a static
  `CGO_ENABLED=0` build.
- `syncBuffer` (mutex-wrapped `bytes.Buffer`) guards concurrent writes from the command goroutine
  and reads from `GetOutput`. Preserve this when touching job I/O — tests run with `-race`.

## e2e suite (`test/e2e/`)

- Builds the Docker image (or reuses `BEZALEL_IMAGE` if set — much faster when iterating) and
  runs bezalel in a container with a host tempdir mounted at `/workspace` and
  `BEZALEL_AUTH_TOKEN` set. Tests drive the server with the `mark3labs/mcp-go` Streamable HTTP
  client and validate side effects by reading the mounted volume directly on the host.
- **Container runs as the host UID/GID** (`-u $(id -u):$(id -g)`); this is required so that
  files the server writes into the volume are removable by `t.TempDir()` cleanup. Without it,
  root-owned files cause `unlinkat: permission denied` on teardown.
- `TestMain` skips the whole suite (exit 0) if `docker` is not on PATH.
- Host port is assigned dynamically (`-p 127.0.0.1::8080`) and discovered via `docker port`.

## Conventions

- Standard Go style; gofmt is enforced strictly in CI. Package-level doc comment on each package.
- Exported identifiers documented with `// Name ...` comments.
- Unit tests are black-box (`package server_test`, `package shell_test`) using `httptest` and the
  `rpcCall`/`rawPost` helpers in `server_test.go`. Add tests alongside the package as `*_test.go`.
- `slog` for logging (stderr, text handler).

## Status

Early-stage (README says "initial spike"). `serverVersion` is `0.1.0`. The current branch
(`add-token-auth`) is for adding token authentication — no auth layer exists in `internal/server`
yet, so an auth check would be wired into `handleMCP` / `ServeHTTP`.
