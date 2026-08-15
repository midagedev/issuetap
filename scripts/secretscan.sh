#!/usr/bin/env bash
# Fail if the tree looks like it contains a live Atlassian credential, a
# non-example atlassian.net host, or an ISSUETAP_REF_* value.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

EXCLUDE=(
  --exclude-dir=.git
  --exclude-dir=node_modules
  --exclude-dir=dist
  --exclude-dir=scratch
  --exclude-dir=.svelte-kit
  --exclude='*.zip'
  --exclude='*.png'
  --exclude='go.sum'
)

fail=0

# grep exit codes: 0 = match (a finding), 1 = no match (clean), >=2 = grep
# itself failed. A broken scanner must fail closed, not report clean —
# GNU grep rejected the first version of the token pattern (`\-` formed a
# reverse range inside the bracket) and the scan silently passed on Linux.
scan() {
  local label="$1"; shift
  local rc=0
  "$@" || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    echo "secretscan: $label" >&2
    fail=1
  elif [[ "$rc" -ge 2 ]]; then
    echo "secretscan: grep failed (rc=$rc) while scanning for: $label" >&2
    exit 2
  fi
}

# Atlassian Cloud API tokens (ATATT…) and scoped tokens (ATCTT…).
# `-` sits last in the bracket so GNU and BSD grep read it literally.
scan "Atlassian API token shape" \
  grep -RInE "${EXCLUDE[@]}" 'ATATT[A-Za-z0-9_=+/-]+|ATCTT[A-Za-z0-9_=+/-]+' .

# Real site hosts. example.atlassian.net is the documented redaction stand-in.
# No pipe: a pipeline would let a grep error (rc 2) be masked by the filter
# stage's rc 1 under pipefail and read as clean.
scan_hosts() {
  local hosts rc=0
  hosts=$(grep -RInE "${EXCLUDE[@]}" 'https?://[A-Za-z0-9.-]+\.atlassian\.net' .) || rc=$?
  if [[ "$rc" -ge 2 ]]; then
    return "$rc"
  fi
  if [[ "$rc" -eq 1 || -z "$hosts" ]]; then
    return 1
  fi
  grep -v 'example.atlassian.net' <<<"$hosts"
}
scan "non-example atlassian.net host" scan_hosts

# Never persist the live reference-site env names with a value.
scan "ISSUETAP_REF_* assignment" \
  grep -RInE "${EXCLUDE[@]}" 'ISSUETAP_REF_(SITE|EMAIL|TOKEN)=' .

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "secretscan: clean"
exit 0
