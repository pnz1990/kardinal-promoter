#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# Fails when a tracked file matching one of the git pathspecs has no Apache
# 2.0 header. CI runs it for the Go sources and the web sources.
#
#   hack/check-license-headers.sh '*.go'
#   hack/check-license-headers.sh 'web/src/*.ts' 'web/src/*.tsx'
#
# Run it from the repository root (or any git work tree). No matching file is
# not an error; a file grep cannot read is, so a broken check never passes.
set -euo pipefail

[ "$#" -gt 0 ] || { echo "usage: $0 PATHSPEC..." >&2; exit 2; }

header="Licensed under the Apache License"
files=()
while IFS= read -r -d '' f; do
  files+=("$f")
done < <(git ls-files -z -- "$@")

if [ "${#files[@]}" -eq 0 ]; then
  echo "no files match: $*"
  exit 0
fi

missing=()
for f in "${files[@]}"; do
  # grep exits 1 when the header is absent and 2 on an error such as an
  # unreadable or missing file; only 1 means "no header".
  rc=0
  grep -q -- "$header" "$f" || rc=$?
  case "$rc" in
    0) ;;
    1) missing+=("$f") ;;
    *) echo "error: cannot read $f (grep exit $rc)" >&2; exit 2 ;;
  esac
done

if [ "${#missing[@]}" -gt 0 ]; then
  echo "Missing Apache 2.0 header:"
  printf '%s\n' "${missing[@]}"
  exit 1
fi
echo "${#files[@]} files have the Apache 2.0 header"
