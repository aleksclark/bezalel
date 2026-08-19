#!/usr/bin/env bash
# Bezalel Stacklane compose lifecycle.
# Always uses: docker compose -p "bezalel-<instance>" -f "$ROOT/docker-compose.yml"
set -euo pipefail

# Neutralize accidental ambient Compose controls before assigning locals.
# STACKLANE_INSTANCE remains a documented operator input and is derived below.
unset COMPOSE_FILE COMPOSE_PROFILES COMPOSE_PROJECT_NAME

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT}/docker-compose.yml"
CHECK_SCRIPT="${ROOT}/scripts/compose-dev-check.sh"
PROJECT_SLUG="bezalel"

die() { echo "error: $*" >&2; exit 1; }
info() { echo "bezalel-compose: $*" >&2; }

# with_timeout uses GNU/coreutils timeout when available and a Python fallback on macOS.
with_timeout() {
  local duration="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$duration" "$@"
    return
  fi
  command -v python3 >/dev/null 2>&1 || die "timeout or python3 required"
  python3 - "$duration" "$@" <<'PY'
import subprocess
import sys

raw, command = sys.argv[1], sys.argv[2:]
try:
    seconds = float(raw[:-1]) if raw.endswith("s") else float(raw)
    sys.exit(subprocess.run(command, timeout=seconds).returncode)
except subprocess.TimeoutExpired:
    sys.exit(124)
except ValueError:
    sys.exit(125)
PY
}

# sanitize_instance: lowercase, non [a-z0-9-] → -, collapse dashes, trim, max 48, fallback dev
sanitize_instance() {
  local s="${1:-}"
  s="$(printf '%s' "$s" | tr '[:upper:]' '[:lower:]')"
  s="$(printf '%s' "$s" | sed -E 's/[^a-z0-9-]+/-/g; s/-+/-/g; s/^-+//; s/-+$//')"
  if [[ ${#s} -gt 48 ]]; then
    s="${s:0:48}"
    s="$(printf '%s' "$s" | sed -E 's/-+$//')"
  fi
  if [[ -z "$s" ]]; then
    s="dev"
  fi
  printf '%s' "$s"
}

derive_instance() {
  if [[ -n "${STACKLANE_INSTANCE:-}" ]]; then
    sanitize_instance "$STACKLANE_INSTANCE"
    return
  fi
  local wt
  wt="$(basename "$ROOT")"
  if [[ -n "$wt" && "$wt" != "." && "$wt" != "/" ]]; then
    sanitize_instance "$wt"
    return
  fi
  local branch=""
  if command -v git >/dev/null 2>&1; then
    branch="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
  fi
  if [[ -n "$branch" && "$branch" != "HEAD" ]]; then
    sanitize_instance "$branch"
    return
  fi
  sanitize_instance "dev"
}

detect_base_domain() {
  if [[ -n "${STACKLANE_BASE_DOMAIN:-}" ]]; then
    printf '%s' "$STACKLANE_BASE_DOMAIN"
    return
  fi
  if ! command -v stacklane >/dev/null 2>&1; then
    printf 'test'
    return
  fi
  local detected=""
  detected="$(
    with_timeout 3s stacklane status -o json 2>/dev/null \
      | python3 -c 'import json,sys,re
try:
    data=json.load(sys.stdin)
except Exception:
    sys.exit(0)
val=data.get("base_domain") if isinstance(data, dict) else ""
if isinstance(val, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,126}", val):
    print(val)
' || true
  )"
  if [[ -n "$detected" ]]; then
    printf '%s' "$detected"
    return
  fi
  printf 'test'
}

require_docker() {
  command -v docker >/dev/null 2>&1 || die "docker not found"
  docker compose version >/dev/null 2>&1 || die "docker compose not available"
  [[ -f "$COMPOSE_FILE" ]] || die "missing $COMPOSE_FILE"
}

compose() {
  docker compose -p "$COMPOSE_PROJECT" --project-directory "$ROOT" -f "$COMPOSE_FILE" "$@"
}

export_stack_env() {
  INSTANCE="$(derive_instance)"
  COMPOSE_PROJECT="${PROJECT_SLUG}-${INSTANCE}"
  STACKLANE_BASE_DOMAIN="$(detect_base_domain)"
  export STACKLANE_INSTANCE="$INSTANCE"
  export STACKLANE_BASE_DOMAIN
  export COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT"
}

host_port_for() {
  local svc="$1"
  local target="$2"
  local mapping
  mapping="$(compose port "$svc" "$target" 2>/dev/null || true)"
  if [[ -z "$mapping" ]]; then
    printf ''
    return
  fi
  printf '%s' "${mapping##*:}"
}

stacklane_status_line() {
  if ! command -v stacklane >/dev/null 2>&1; then
    printf 'stacklane: BLOCKED (daemon/cli absent — direct loopback ports still work)\n'
    return
  fi
  if with_timeout 3s stacklane status >/dev/null 2>&1; then
    local mcp_fqdn="mcp.${INSTANCE}.${PROJECT_SLUG}.${STACKLANE_BASE_DOMAIN}"
    if with_timeout 3s stacklane resolve "$mcp_fqdn" >/dev/null 2>&1; then
      printf 'stacklane: OK\n'
    else
      printf 'stacklane: degraded (daemon up; %s not resolved yet)\n' "$mcp_fqdn"
    fi
  else
    printf 'stacklane: BLOCKED (daemon not reachable)\n'
  fi
}

print_endpoints() {
  local mcp_hp base
  mcp_hp="$(host_port_for mcp 8080)"
  base="${STACKLANE_BASE_DOMAIN}"

  echo "mcp.${INSTANCE}.${PROJECT_SLUG}.${base}:8080  (via Stacklane VIP)"
  if [[ -n "$mcp_hp" ]]; then
    echo "direct mcp:    http://127.0.0.1:${mcp_hp}/mcp"
    echo "direct health: http://127.0.0.1:${mcp_hp}/health"
  else
    echo "direct mcp:    (not published — stack down?)"
  fi
  stacklane_status_line
  echo "instance: ${INSTANCE}"
  echo "compose project: ${COMPOSE_PROJECT}"
  echo "stacklane base_domain: ${base}"
}

wait_healthy() {
  local timeout_s="${1:-360}"
  local start now elapsed
  local cid="${COMPOSE_PROJECT}-mcp-1"
  start="$(date +%s)"
  info "waiting for mcp healthy (timeout ${timeout_s}s)…"
  while true; do
    now="$(date +%s)"
    elapsed=$((now - start))
    if (( elapsed > timeout_s )); then
      compose ps || true
      die "mcp not healthy within ${timeout_s}s"
    fi
    local mcp_h
    mcp_h="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || echo missing)"
    if [[ "$mcp_h" == "healthy" ]]; then
      info "mcp healthy"
      return 0
    fi
    sleep 2
  done
}

cmd_check() {
  require_docker
  export_stack_env
  command -v python3 >/dev/null 2>&1 || die "python3 required for compose check"
  bash "$CHECK_SCRIPT"
}

cmd_up() {
  require_docker
  export_stack_env
  bash "$CHECK_SCRIPT"
  info "building images (project=${COMPOSE_PROJECT} instance=${INSTANCE})…"
  compose build
  info "starting stack…"
  compose up -d --remove-orphans
  wait_healthy 360
  print_endpoints
}

cmd_status() {
  require_docker
  export_stack_env
  compose ps
  echo
  print_endpoints
}

cmd_logs() {
  require_docker
  export_stack_env
  # Ctrl-C stops following only; it does not tear the stack down.
  compose logs -f "$@"
}

cmd_down() {
  require_docker
  export_stack_env
  info "stopping stack (volumes preserved; never uses -v)…"
  compose down --remove-orphans
}

cmd_destroy() {
  export_stack_env
  local expect="${COMPOSE_PROJECT}-destroy"
  if [[ "${CONFIRM:-}" != "$expect" ]]; then
    die "refusing destroy: set CONFIRM=${expect} to remove volumes for project ${COMPOSE_PROJECT}"
  fi
  require_docker
  info "destroying stack AND volumes for ${COMPOSE_PROJECT}…"
  compose down -v --remove-orphans
}

cmd_endpoints() {
  require_docker
  export_stack_env
  print_endpoints
}

usage() {
  cat <<'EOF'
Usage: scripts/compose-dev.sh <command>

Commands:
  check       Fail-closed Stacklane/compose contract validation
  up          check + build + up -d + wait healthy + print endpoints
  status      compose ps + endpoint table
  endpoints   print FQDNs + direct loopback mappings
  logs        follow compose logs (Ctrl-C leaves the stack running)
  down        compose down (never -v; volumes preserved)
  destroy     compose down -v (requires CONFIRM=<compose-project>-destroy)

Environment:
  STACKLANE_INSTANCE     override instance slug (else worktree dirname / branch / dev)
  STACKLANE_BASE_DOMAIN  FQDN base (default: host daemon base_domain, else test)
  CONFIRM                required for destroy; must equal bezalel-<instance>-destroy

Notes:
  - Host fallback is unchanged: go run ./cmd/bezalel --port 8080
  - Production Dockerfile is unchanged.
  - No auth tokens or provider secrets are interpolated into compose.
  - Stacklane daemon is optional; direct 127.0.0.1 ephemeral ports always work.
  - Compose project is always bezalel-<instance> via `docker compose -p`.
EOF
}

main() {
  local cmd="${1:-}"
  shift || true
  case "$cmd" in
    check) cmd_check "$@" ;;
    up) cmd_up "$@" ;;
    status) cmd_status "$@" ;;
    endpoints) cmd_endpoints "$@" ;;
    logs) cmd_logs "$@" ;;
    down) cmd_down "$@" ;;
    destroy) cmd_destroy "$@" ;;
    -h|--help|help|"") usage; [[ -n "$cmd" ]] || exit 1 ;;
    *) die "unknown command: $cmd (try --help)" ;;
  esac
}

main "$@"
