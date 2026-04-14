#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <goos> <goarch> <version> <output-dir>" >&2
  exit 64
fi

goos="$1"
goarch="$2"
version="${3#v}"
outdir="$4"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root_dir="$(cd "${script_dir}/../.." && pwd)"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/brain-release.XXXXXX")"
trap 'rm -rf "${tmpdir}"' EXIT

stage_name="brain_${version}_${goos}_${goarch}"
stage_dir="${tmpdir}/${stage_name}"
mkdir -p "${stage_dir}" "${outdir}"
outdir="$(cd "${outdir}" && pwd)"

build_commit="${BUILD_COMMIT:-$(git -C "${root_dir}" rev-parse --short=12 HEAD)}"
build_time="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
ldflags="-s -w -X github.com/leef-l/brain.BuildCommit=${build_commit} -X github.com/leef-l/brain.BuildTime=${build_time}"

ext=""
if [[ "${goos}" == "windows" ]]; then
  ext=".exe"
fi

declare -a binaries=(
  "brain=./cmd"
  "brain-central=./cmd/brain-central"
  "brain-data=./cmd/brain-data"
  "brain-quant=./cmd/brain-quant"
  "brain-code=./cmd/brain-code"
  "brain-verifier=./cmd/brain-verifier"
  "brain-fault=./cmd/brain-fault"
  "brain-browser=./cmd/brain-browser"
  "exchange-executor=./cmd/exchange-executor"
)

for entry in "${binaries[@]}"; do
  name="${entry%%=*}"
  pkg="${entry#*=}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${ldflags}" -o "${stage_dir}/${name}${ext}" "${pkg}"
done

cp "${root_dir}/bin/config.example.json" "${stage_dir}/"
cp "${root_dir}/bin/keybindings.example.json" "${stage_dir}/"
cp "${root_dir}/bin/quant.example.yaml" "${stage_dir}/"
cp "${root_dir}/VERSION.json" "${stage_dir}/"
cp "${root_dir}/LICENSE" "${stage_dir}/"
cp "${root_dir}/README.md" "${stage_dir}/"
cp "${root_dir}/CHANGELOG.md" "${stage_dir}/"
cp "${root_dir}/SECURITY.md" "${stage_dir}/"
mkdir -p "${stage_dir}/persistence" "${stage_dir}/scripts/ops" "${stage_dir}/docs/quant"
cp -R "${root_dir}/persistence/migrations" "${stage_dir}/persistence/"
cp "${root_dir}/scripts/ops/"*.sh "${stage_dir}/scripts/ops/"
cp "${root_dir}/docs/quant/43-生产交付清单.md" "${stage_dir}/docs/quant/"

(
  cd "${stage_dir}"
  : > MANIFEST.SHA256SUMS
  while IFS= read -r rel; do
    rel="${rel#./}"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "${rel}" >> MANIFEST.SHA256SUMS
    else
      shasum -a 256 "${rel}" >> MANIFEST.SHA256SUMS
    fi
  done < <(find . -type f ! -name MANIFEST.SHA256SUMS | LC_ALL=C sort)
)

archive_path=""
if [[ "${goos}" == "windows" ]]; then
  archive_path="${outdir}/${stage_name}.zip"
  (
    cd "${tmpdir}"
    zip -qr "${archive_path}" "${stage_name}"
  )
else
  archive_path="${outdir}/${stage_name}.tar.gz"
  tar -C "${tmpdir}" -czf "${archive_path}" "${stage_name}"
fi

printf '%s\n' "${archive_path}"
