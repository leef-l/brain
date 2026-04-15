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
ldflags="-s -w -X github.com/leef-l/brain.CLIVersion=${version} -X github.com/leef-l/brain.SDKVersion=${version} -X github.com/leef-l/brain.KernelVersion=${version} -X github.com/leef-l/brain.BuildCommit=${build_commit} -X github.com/leef-l/brain.BuildTime=${build_time}"

ext=""
if [[ "${goos}" == "windows" ]]; then
  ext=".exe"
fi

# --- fixed binaries: CLI + central orchestrator ---
declare -a binaries=(
  "brain=./cmd/brain"
  "brain-central=./central/cmd"
)

# --- auto-discover specialist brains under brains/ ---
# Pattern 1: brains/<name>/cmd/main.go  → brain-<name>
for main in "${root_dir}"/brains/*/cmd/main.go; do
  [[ -f "${main}" ]] || continue
  name="$(basename "$(dirname "$(dirname "${main}")")")"
  binaries+=("brain-${name}=./brains/${name}/cmd")
done

# Pattern 2: brains/<parent>/<sub>/cmd/main.go  → brain-<parent>-<sub>
for main in "${root_dir}"/brains/*/*/cmd/main.go; do
  [[ -f "${main}" ]] || continue
  sub="$(basename "$(dirname "$(dirname "${main}")")")"
  parent="$(basename "$(dirname "$(dirname "$(dirname "${main}")")")")"
  # skip if already matched by pattern 1 (parent has its own cmd/)
  [[ -f "${root_dir}/brains/${parent}/cmd/main.go" ]] && continue
  binaries+=("brain-${parent}-${sub}=./brains/${parent}/${sub}/cmd")
done

# Pattern 3: brains/<name>/cmd/brain-<name>-sidecar/main.go → brain-<name>-sidecar
# These are specialist brain sidecar binaries launched by the Kernel via
# stdio JSON-RPC. Directory is named brain-<name>-sidecar so that
# `go install` produces the correct binary name.
for main in "${root_dir}"/brains/*/cmd/brain-*-sidecar/main.go; do
  [[ -f "${main}" ]] || continue
  bin="$(basename "$(dirname "${main}")")"          # brain-<name>-sidecar
  name="$(echo "${bin}" | sed 's/^brain-//;s/-sidecar$//')"
  binaries+=("${bin}=./brains/${name}/cmd/${bin}")
done

printf 'packaging %d binaries\n' "${#binaries[@]}" >&2

for entry in "${binaries[@]}"; do
  name="${entry%%=*}"
  pkg="${entry#*=}"
  printf '  → %s (%s)\n' "${name}" "${pkg}" >&2
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${ldflags}" -o "${stage_dir}/${name}${ext}" "${pkg}"
done

# --- install to GOPATH/bin (覆盖式) ---
gobin="${GOBIN:-$(go env GOPATH)/bin}"
if [[ -n "${gobin}" ]]; then
  mkdir -p "${gobin}"
  printf 'installing to %s\n' "${gobin}" >&2
  for entry in "${binaries[@]}"; do
    name="${entry%%=*}"
    src="${stage_dir}/${name}${ext}"
    if [[ -f "${src}" ]]; then
      cp -f "${src}" "${gobin}/${name}${ext}"
      printf '  → %s\n' "${gobin}/${name}${ext}" >&2
    fi
  done
fi

cp "${root_dir}/VERSION.json" "${stage_dir}/"
cp "${root_dir}/LICENSE" "${stage_dir}/"
cp "${root_dir}/README.md" "${stage_dir}/"
cp "${root_dir}/CHANGELOG.md" "${stage_dir}/"
cp "${root_dir}/SECURITY.md" "${stage_dir}/"

(
  cd "${stage_dir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./* | sed 's# \./#  #' > MANIFEST.SHA256SUMS
  else
    shasum -a 256 ./* | sed 's# \./#  #' > MANIFEST.SHA256SUMS
  fi
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
