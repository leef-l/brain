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

# 强制清 Go 编译缓存,避免打出旧代码:
#   1) //go:embed 资源不参与默认 build cache key,改前端 static/ 后不清缓存会拿到旧资源
#   2) -ldflags "-X ...BuildCommit=xxx" 不参与 cache key,同 commit 不同分支编译会串
#   3) -trimpath 让不同 worktree 同源码共享缓存
# 注:仅清 build cache 不清 modcache(后者要重新下依赖,过慢)
echo "cleaning Go build cache (not modcache)..." >&2
go clean -cache >/dev/null 2>&1 || true

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
