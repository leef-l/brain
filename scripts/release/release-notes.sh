#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <version>" >&2
  exit 64
fi

version="${1#v}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root_dir="$(cd "${script_dir}/../.." && pwd)"
changelog="${root_dir}/CHANGELOG.md"

section="$(
  awk -v version="${version}" '
    $0 ~ "^## \\[" version "\\]" {
      emit = 1
    }
    emit && $0 ~ "^## \\[" && $0 !~ "^## \\[" version "\\]" {
      exit
    }
    emit {
      print
    }
  ' "${changelog}"
)"

if [[ -z "${section//[[:space:]]/}" ]]; then
  echo "release-notes: version ${version} not found in CHANGELOG.md" >&2
  exit 1
fi

printf '%s\n' "${section}"
