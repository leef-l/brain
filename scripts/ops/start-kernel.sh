#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root_dir="$(cd "${script_dir}/../.." && pwd)"
listen="${BRAIN_LISTEN:-127.0.0.1:7701}"
log_dir="${BRAIN_LOG_DIR:-${root_dir}/.tmp/ops}"
log_file="${BRAIN_LOG_FILE:-${log_dir}/brain-serve.log}"
pid_file="${BRAIN_PID_FILE:-${log_dir}/brain-serve.pid}"
mode="${BRAIN_PERMISSION_MODE:-restricted}"
workdir_policy="${BRAIN_RUN_WORKDIR_POLICY:-confined}"
default_binary="${root_dir}/brain"
if [[ ! -x "${default_binary}" ]]; then
  default_binary="${root_dir}/bin/brain"
fi
binary="${BRAIN_BINARY:-${default_binary}}"

mkdir -p "${log_dir}"

if [[ ! -x "${binary}" ]]; then
  echo "brain binary not found or not executable: ${binary}" >&2
  echo "build it first: go build -o ${root_dir}/bin/brain ./cmd" >&2
  exit 1
fi

if [[ "${1:-}" == "foreground" ]]; then
  exec "${binary}" serve \
    --listen "${listen}" \
    --mode "${mode}" \
    --run-workdir-policy "${workdir_policy}" \
    --log-file "${log_file}"
fi

if [[ -f "${pid_file}" ]] && kill -0 "$(cat "${pid_file}")" 2>/dev/null; then
  echo "brain serve already running: pid=$(cat "${pid_file}")"
  exit 0
fi

nohup "${binary}" serve \
  --listen "${listen}" \
  --mode "${mode}" \
  --run-workdir-policy "${workdir_policy}" \
  --log-file "${log_file}" \
  >/dev/null 2>&1 &

echo $! > "${pid_file}"
echo "started brain serve"
echo "listen=${listen}"
echo "pid=$(cat "${pid_file}")"
echo "log=${log_file}"
