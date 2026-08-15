# Issuetap Agent Instructions

Issuetap uses spec-driven development. Do not start implementation before
checking the relevant spec artifacts.

## Required Reading Order

1. `specs/000-product/spec.md`
2. `specs/000-product/plan.md`
3. `specs/000-product/tasks.md`
4. `specs/000-product/gates.md`
5. `docs/CONCEPT.md`
6. `docs/COMPATIBILITY.md`
7. `docs/LOCALES.md`
8. `docs/PRODUCTION_BOUNDARIES.md`

## Development Rules

- Specs drive implementation. If code and spec disagree, update the spec
  first or explicitly record a decision.
- Issuetap is not an issue tracker. Do not add planning features.
- Status, priority, and issue type logic keys on ids or `statusCategory`,
  never on a localized display name.
- Known unimplemented Atlassian routes return `unsupported_endpoint` (501),
  never a 404 and never a plausible lie.
- No outbound network calls at runtime.
- Never write live reference-site host, email, or token to disk. Redact
  fixtures before they land. `make secretscan` must stay green.
- GET only against any real Atlassian site. Mutations corrupt published
  material.
- Fixture + seed must be deterministic. Do not call `time.Now()` for
  issued timestamps.
- Agent work must declare ownership, verification, and gate status.

## Agent Handoff

Every agent handoff should include:

- Summary
- Files changed
- Verification
- Open risks
- Gate status
