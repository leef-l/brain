#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <version> [output-dir]" >&2
  exit 64
fi

version="${1#v}"
outdir="${2:-dist}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

declare -a default_targets=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
  "freebsd/amd64"
)

declare -a targets=()
if [[ -n "${RELEASE_TARGETS:-}" ]]; then
  read -r -a targets <<< "${RELEASE_TARGETS}"
else
  targets=("${default_targets[@]}")
fi

mkdir -p "${outdir}"
rm -f "${outdir}"/brain_* "${outdir}"/SHA256SUMS

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  "${script_dir}/package.sh" "${goos}" "${goarch}" "${version}" "${outdir}"
done

(
  cd "${outdir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./brain_* | sed 's# \./#  #' | LC_ALL=C sort > SHA256SUMS
  else
    shasum -a 256 ./brain_* | sed 's# \./#  #' | LC_ALL=C sort > SHA256SUMS
  fi
)

find "${outdir}" -maxdepth 1 -type f | sort
