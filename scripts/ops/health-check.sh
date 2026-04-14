#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root_dir="$(cd "${script_dir}/../.." && pwd)"
default_bundle_dir="${root_dir}"
if [[ -x "${root_dir}/bin/brain" ]]; then
  default_bundle_dir="${root_dir}/bin"
fi
explicit_bundle_dir=0
if [[ $# -ge 1 ]]; then
  explicit_bundle_dir=1
fi
bundle_dir="${1:-${default_bundle_dir}}"
health_url="${BRAIN_HEALTH_URL:-}"
run_doctor="${BRAIN_RUN_DOCTOR:-0}"
doctor_home_mode="${BRAIN_DOCTOR_HOME_MODE:-isolated}"

has_command() {
  command -v "$1" >/dev/null 2>&1
}

require_any() {
  local label="$1"
  shift
  local path=""
  for path in "$@"; do
    if [[ -e "${path}" ]]; then
      echo "ok: ${path}"
      return 0
    fi
  done
  echo "missing: ${label}" >&2
  return 1
}

bundle_path() {
  local rel="$1"
  if [[ "${explicit_bundle_dir}" == "1" ]]; then
    printf '%s\n' "${bundle_dir}/${rel}"
    return 0
  fi
  printf '%s\n' \
    "${bundle_dir}/${rel}" \
    "${root_dir}/${rel}" \
    "${root_dir}/bin/${rel}"
}

verify_manifest() {
  local manifest_path=""
  if [[ -f "${bundle_dir}/MANIFEST.SHA256SUMS" ]]; then
    manifest_path="${bundle_dir}/MANIFEST.SHA256SUMS"
  elif [[ "${explicit_bundle_dir}" != "1" && -f "${root_dir}/MANIFEST.SHA256SUMS" ]]; then
    manifest_path="${root_dir}/MANIFEST.SHA256SUMS"
  else
    return 0
  fi

  echo "ok: ${manifest_path}"
  local manifest_dir
  manifest_dir="$(cd "$(dirname "${manifest_path}")" && pwd)"
  if has_command sha256sum; then
    (cd "${manifest_dir}" && sha256sum -c "$(basename "${manifest_path}")")
  elif has_command shasum; then
    (cd "${manifest_dir}" && shasum -a 256 -c "$(basename "${manifest_path}")")
  else
    echo "checksum tool missing: sha256sum or shasum is required to verify ${manifest_path}" >&2
    return 1
  fi
}

run_brain_doctor() {
  if [[ "${run_doctor}" != "1" ]]; then
    return 0
  fi

  local brain_bin=""
  if [[ -x "${bundle_dir}/brain" ]]; then
    brain_bin="${bundle_dir}/brain"
  elif [[ "${explicit_bundle_dir}" != "1" && -x "${root_dir}/bin/brain" ]]; then
    brain_bin="${root_dir}/bin/brain"
  elif [[ "${explicit_bundle_dir}" != "1" && -x "${root_dir}/brain" ]]; then
    brain_bin="${root_dir}/brain"
  else
    echo "brain doctor skipped: brain binary not found" >&2
    return 1
  fi

  case "${doctor_home_mode}" in
    isolated)
      local doctor_home=""
      doctor_home="$(mktemp -d "${TMPDIR:-/tmp}/brain-doctor-home.XXXXXX")"
      mkdir -p "${doctor_home}"
      if HOME="${doctor_home}" USERPROFILE="${doctor_home}" XDG_CONFIG_HOME="${doctor_home}/.config" \
        "${brain_bin}" doctor; then
        rm -rf "${doctor_home}"
        return 0
      fi
      local status=$?
      rm -rf "${doctor_home}"
      return "${status}"
      ;;
    host)
      "${brain_bin}" doctor
      ;;
    *)
      echo "invalid BRAIN_DOCTOR_HOME_MODE: ${doctor_home_mode}" >&2
      return 1
      ;;
  esac
}

status=0

for rel in \
  "brain" \
  "brain-central" \
  "brain-data" \
  "brain-quant" \
  "brain-code" \
  "brain-verifier" \
  "brain-fault" \
  "brain-browser" \
  "exchange-executor"; do
  mapfile -t candidates < <(bundle_path "${rel}")
  require_any "${rel}" "${candidates[@]}" || status=1
done

for rel in \
  "config.example.json" \
  "keybindings.example.json" \
  "quant.example.yaml" \
  "VERSION.json" \
  "LICENSE" \
  "README.md" \
  "CHANGELOG.md" \
  "SECURITY.md" \
  "docs/quant/43-生产交付清单.md" \
  "scripts/ops/apply-migrations.sh" \
  "scripts/ops/health-check.sh" \
  "scripts/ops/start-kernel.sh"; do
  mapfile -t candidates < <(bundle_path "${rel}")
  require_any "${rel}" "${candidates[@]}" || status=1
done

for rel in \
  "persistence/migrations/0001_signal_traces.sql" \
  "persistence/migrations/0002_account_snapshots_daily_reviews.sql"; do
  mapfile -t candidates < <(bundle_path "${rel}")
  require_any "${rel}" "${candidates[@]}" || status=1
done

if ! verify_manifest; then
  status=1
fi

if ! run_brain_doctor; then
  status=1
fi

if [[ -n "${health_url}" ]]; then
  if ! has_command curl; then
    echo "curl is required for HTTP health probe" >&2
    status=1
  elif ! curl --fail --silent --show-error "${health_url}" >/dev/null; then
    echo "health probe failed: ${health_url}" >&2
    status=1
  else
    echo "ok: ${health_url}"
  fi
fi

exit "${status}"
