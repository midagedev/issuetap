# Product Specification: Issuetap

## Status

v0

## Summary

Issuetap is a local, deterministic Atlassian-compatible server for
developing and testing Jira and Confluence clients. It serves Cloud v3
(complete for gadak's call set) and a Data Center v2 read path, with
fixture apply/snapshot, first-class fault scenarios, localized display
names, a Svelte dashboard, and a diagnostics bundle.

It is not an issue tracker.

## Users

- client author (gadak and anyone else talking to Jira/Confluence)
- QA writing CI scenarios
- agent diagnosing a failed sync

## Core Scenarios

### 1. Gadak syncs from issuetap

A fixture is loaded. gadak is pointed at `http://127.0.0.1:<port>` with a
throwaway profile. `gadak sync` mirrors issues, comments, changelog, and
Confluence pages. Counts match the fixture.

### 2. Localized names break a name-keyed client

`--locale ko` serves `진행 중` / `작업`. `status = "In Progress"` is
empty. `status = 3` hits.

### 3. Credential revoked mid-run

A scenario returns 401 from request N. The client stops. A client that
retries forever fails the scenario.

## Non-goals

- Being a usable issue tracker
- Full JQL / CQL
- Boards, dashboards, Forge apps
- Verified Data Center parity
- Outbound calls to a real site at runtime
