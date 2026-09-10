#!/usr/bin/env bash
# Fail if any tracked Go file is not gofmt-clean.
#
# The single owner of the formatting gate: `make fmt` locally and the CI
# "Go fmt" job both run this script, so the two can never disagree.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Walk the tree ourselves; gofmt has no exclude flag and node_modules/dist
# are not ours to format.
files=$(find . \
  -name .git -prune -o \
  -name node_modules -prune -o \
  -name dist -prune -o \
  -name scratch -prune -o \
  -name '*.go' -print)

if [ -z "$files" ]; then
  echo "fmtcheck: no Go files found — is this the right tree?" >&2
  exit 1
fi

# shellcheck disable=SC2086
drift=$(gofmt -l $files)

if [ -n "$drift" ]; then
  echo "fmtcheck: FAIL — these files are not gofmt-clean:" >&2
  echo "$drift" | sed 's/^/  /' >&2
  echo >&2
  echo "Fix with one line:" >&2
  echo "  gofmt -w ." >&2
  exit 1
fi

echo "fmtcheck: OK — all Go files are gofmt-clean"
