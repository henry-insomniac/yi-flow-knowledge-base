#!/usr/bin/env bash
set -euo pipefail

DOMAIN="${RAGFLOW_DOMAIN:-rag.yi-flow.com}"
MIN_CPU="${RAGFLOW_MIN_CPU:-4}"
MIN_MEM_MB="${RAGFLOW_MIN_MEM_MB:-16384}"
MIN_DISK_GB="${RAGFLOW_MIN_DISK_GB:-50}"
MIN_MAP_COUNT="${RAGFLOW_MIN_MAP_COUNT:-262144}"
MIN_DOCKER_VERSION="${RAGFLOW_MIN_DOCKER_VERSION:-24.0.0}"
MIN_COMPOSE_VERSION="${RAGFLOW_MIN_COMPOSE_VERSION:-2.26.1}"
EXPECTED_A_RECORD="${RAGFLOW_EXPECTED_A_RECORD:-}"

failures=0

version_ge() {
  python3 - "$1" "$2" <<'PY'
import re
import sys

def parse(value):
    match = re.search(r"(\d+(?:\.\d+){0,3})", value)
    if not match:
        return None
    parts = [int(part) for part in match.group(1).split(".")]
    return parts + [0] * (4 - len(parts))

actual = parse(sys.argv[1])
minimum = parse(sys.argv[2])
if actual is None or minimum is None or actual < minimum:
    raise SystemExit(1)
PY
}

check_min() {
  local name="$1"
  local value="$2"
  local minimum="$3"
  if [[ "$value" =~ ^[0-9]+$ ]] && (( value >= minimum )); then
    echo "ragflow_readiness_ok ${name}=${value} required>=${minimum}"
  else
    echo "ragflow_readiness_failed ${name}=${value:-unknown} required>=${minimum}" >&2
    failures=$((failures + 1))
  fi
}

kernel="$(uname -s 2>/dev/null || echo unknown)"
arch="$(uname -m 2>/dev/null || echo unknown)"
cpu_count="$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 0)"
if [[ -r /proc/meminfo ]]; then
  mem_mb="$(awk '/MemTotal/ {printf "%d", $2 / 1024}' /proc/meminfo 2>/dev/null || echo 0)"
elif mem_bytes="$(sysctl -n hw.memsize 2>/dev/null)"; then
  mem_mb="$((mem_bytes / 1024 / 1024))"
else
  mem_mb=0
fi
disk_gb="$(df -Pk / 2>/dev/null | awk 'NR==2 {printf "%d", $4 / 1024 / 1024}' || echo 0)"
map_count="$(sysctl -n vm.max_map_count 2>/dev/null || echo 0)"

check_min "cpu" "$cpu_count" "$MIN_CPU"
case "$arch" in
  x86_64|amd64|arm64|aarch64)
    echo "ragflow_readiness_ok arch=${arch}"
    ;;
  *)
    echo "ragflow_readiness_info arch=${arch} verify_ragflow_image_support"
    ;;
esac
check_min "mem_mb" "$mem_mb" "$MIN_MEM_MB"
check_min "disk_free_gb" "$disk_gb" "$MIN_DISK_GB"
if [[ "$map_count" == "0" && "$kernel" == "Darwin" ]]; then
  echo "ragflow_readiness_info vm_max_map_count=not_applicable_on_darwin target_host_must_check_linux"
else
  check_min "vm_max_map_count" "$map_count" "$MIN_MAP_COUNT"
fi

if docker_version="$(docker --version 2>/dev/null)"; then
  if version_ge "$docker_version" "$MIN_DOCKER_VERSION"; then
    echo "ragflow_readiness_ok docker=${docker_version} required>=${MIN_DOCKER_VERSION}"
  else
    echo "ragflow_readiness_failed docker=${docker_version} required>=${MIN_DOCKER_VERSION}" >&2
    failures=$((failures + 1))
  fi
else
  echo "ragflow_readiness_failed docker=missing" >&2
  failures=$((failures + 1))
fi

if compose_version="$(docker compose version 2>/dev/null)"; then
  if version_ge "$compose_version" "$MIN_COMPOSE_VERSION"; then
    echo "ragflow_readiness_ok compose=${compose_version} required>=${MIN_COMPOSE_VERSION}"
  else
    echo "ragflow_readiness_failed compose=${compose_version} required>=${MIN_COMPOSE_VERSION}" >&2
    failures=$((failures + 1))
  fi
else
  echo "ragflow_readiness_failed compose=missing" >&2
  failures=$((failures + 1))
fi

if command -v dig >/dev/null 2>&1; then
  records="$(dig +short "$DOMAIN" A || true)"
  ttl="$(dig +nocmd "$DOMAIN" A +noall +answer 2>/dev/null | awk 'NR==1 {print $2}' || true)"
elif command -v getent >/dev/null 2>&1; then
  records="$(getent ahostsv4 "$DOMAIN" | awk '{print $1}' | sort -u || true)"
  ttl=""
else
  records=""
  ttl=""
fi
if [[ -n "$records" ]]; then
  joined_records="$(echo "$records" | paste -sd, -)"
  echo "ragflow_readiness_ok dns ${DOMAIN}=${joined_records} ttl=${ttl:-unknown}"
  if [[ -n "$EXPECTED_A_RECORD" ]] && ! grep -qx "$EXPECTED_A_RECORD" <<< "$records"; then
    echo "ragflow_readiness_failed dns_expected_a=${EXPECTED_A_RECORD} actual=${joined_records}" >&2
    failures=$((failures + 1))
  fi
else
  echo "ragflow_readiness_failed dns ${DOMAIN}=missing" >&2
  failures=$((failures + 1))
fi

if command -v ss >/dev/null 2>&1; then
  public_listeners="$(ss -ltnH 2>/dev/null | awk '{print $4}' | sed 's/.*://' | sort -n | uniq | paste -sd, -)"
  echo "ragflow_readiness_info listening_tcp_ports=${public_listeners:-none}"
fi

if [[ -n "${RAGFLOW_POSTGRES_DSN:-}" ]]; then
  if command -v psql >/dev/null 2>&1; then
    if psql "$RAGFLOW_POSTGRES_DSN" -Atq -c 'select 1' >/tmp/ragflow-pg-readiness 2>/tmp/ragflow-pg-readiness.err && grep -qx '1' /tmp/ragflow-pg-readiness; then
      echo "ragflow_readiness_ok postgres=reachable"
    else
      echo "ragflow_readiness_failed postgres=unreachable" >&2
      failures=$((failures + 1))
    fi
  else
    echo "ragflow_readiness_failed postgres=psql_missing" >&2
    failures=$((failures + 1))
  fi
elif [[ -n "${PGHOST:-}" || -n "${PGDATABASE:-}" || -n "${PGUSER:-}" ]]; then
  if command -v psql >/dev/null 2>&1; then
    if psql -Atq -c 'select 1' >/tmp/ragflow-pg-readiness 2>/tmp/ragflow-pg-readiness.err && grep -qx '1' /tmp/ragflow-pg-readiness; then
      echo "ragflow_readiness_ok postgres=reachable"
    else
      echo "ragflow_readiness_failed postgres=unreachable" >&2
      failures=$((failures + 1))
    fi
  else
    echo "ragflow_readiness_failed postgres=psql_missing" >&2
    failures=$((failures + 1))
  fi
else
  echo "ragflow_readiness_info postgres=skipped set_RAGFLOW_POSTGRES_DSN_or_PG_env"
fi

if (( failures > 0 )); then
  echo "ragflow_readiness_failed failures=${failures}" >&2
  exit 1
fi

echo "ragflow_readiness_ok failures=0"
