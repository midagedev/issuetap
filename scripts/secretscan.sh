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

# Atlassian Cloud API tokens (ATATT…) and scoped tokens (ATCTT…).
if grep -RInE "${EXCLUDE[@]}" 'ATATT[A-Za-z0-9_\-=+/]+|ATCTT[A-Za-z0-9_\-=+/]+' . ; then
  echo "secretscan: Atlassian API token shape" >&2
  fail=1
fi

# Real site hosts. example.atlassian.net is the documented redaction stand-in.
if grep -RInE "${EXCLUDE[@]}" 'https?://[A-Za-z0-9.-]+\.atlassian\.net' . \
  | grep -v 'example.atlassian.net' ; then
  echo "secretscan: non-example atlassian.net host" >&2
  fail=1
fi

# Never persist the live reference-site env names with a value.
if grep -RInE "${EXCLUDE[@]}" 'ISSUETAP_REF_(SITE|EMAIL|TOKEN)=' . ; then
  echo "secretscan: ISSUETAP_REF_* assignment" >&2
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "secretscan: clean"
exit 0
