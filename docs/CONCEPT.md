# Issuetap Concept

## One-Line Description

Issuetap is a local, deterministic Atlassian-compatible server for developing
and testing Jira and Confluence clients, with first-class fault injection and
localized display names.

## Positioning

Issuetap is not an issue tracker and not a Jira clone.

It is a lab instrument. A client talks to it the way it talks to Atlassian
Cloud (or, on the read path, Data Center). The dashboard shows the requests.
Scenarios inject the faults that a live site produces at the worst time.

The two-lane model:

- **Fast lane** — issuetap for local development, unit and e2e tests, CI
  regression, fixture setup, fault scenarios.
- **Fallback lane** — a real Atlassian Cloud site for provider-specific
  behaviour and final compatibility checks.

## Why Existing Tools Are Not Enough

`httptest.NewServer` in a client repo works until it doesn't:

- every test file grows its own route table
- response shapes drift from the live API
- nobody injects a mid-sync 401 or a 429 with `Retry-After`
- nobody serves `진행 중` for the same status id that English calls
  `In Progress`

Issuetap is the third product in the billtap / dogtap family: a local
stand-in for one vendor API, honest about coverage, with fixtures,
scenarios, and a diagnostics bundle.

## Core Product Loop

1. Write or pick a fixture.
2. `issuetap serve --fixture …`
3. Point the client at `http://127.0.0.1:8080`.
4. Watch the request log. If the client is wrong, the log says so.
5. Run a scenario (revoked credential, 429, Korean names) in CI.
6. If something is opaque, `issuetap diagnose`.

## Product Philosophy

### Determinism matters

Same fixture + same seed → same ids, timestamps, and ordering. CI cannot
depend on `time.Now()`.

### Honesty matters

A known unimplemented route returns `unsupported_endpoint`, not a 404 and
not a plausible lie. A consuming test must be able to tell "issuetap does
not cover this yet" apart from "my client is broken".

### Names are not keys

Atlassian localizes display names per account. Issuetap serves the same
data under `--locale ko` (and `ja`, `de`) so a client that keys on
`"Task"` or `"In Progress"` fails here instead of in production.

### No outbound network

The process never calls Atlassian, or anything else, at runtime.
