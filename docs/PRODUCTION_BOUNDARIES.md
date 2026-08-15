# Production Boundaries

Issuetap is a local testbed. It must never be mistaken for a real Jira.

## What happens if someone points production at it

- Every response carries `X-Issuetap: 1`.
- `GET /rest/api/3/serverInfo` returns `serverTitle: "issuetap"`.
- There is no durable store. A process restart drops the graph unless a
  fixture is re-applied.
- There is no outbound network. Issuetap cannot reach a real site, a
  mailer, or a webhook consumer on its own.
- Writes mutate the in-memory fixture only. They do not create work in
  anyone's real project.
- Auth is whatever `--email` / `--token` you passed, or *any* non-empty
  Basic/Bearer pair by default. That is convenient for tests and fatal
  as a production control.

If a production load balancer is aimed at issuetap, users will see the
fixture's fake issues, localized names from `--locale`, and whatever
faults a scenario left armed. They will not see their Jira.

## Allowed uses

- Local development of a Jira/Confluence client.
- CI: fixture apply, scenario run, gadak-style sync.
- Agent diagnostics (`issuetap diagnose`).

## Disallowed uses

- Planning work.
- Replacing Atlassian Cloud or Data Center for a team.
- Holding production credentials. Issuetap accepts the token you
  configure; it must not be a live site token.
- Pointing a production Jira integration at issuetap "just for a minute".

## Default bind

`issuetap serve` listens on `127.0.0.1:8080`. Docker sets
`ISSUETAP_ADDR=0.0.0.0:8080` so the published port works; that image is
still a testbed.

## Secrets

Never write `ISSUETAP_REF_SITE`, `ISSUETAP_REF_EMAIL`, or
`ISSUETAP_REF_TOKEN` to a fixture, a test, a log, or a commit. Fixtures
use `https://example.atlassian.net`, `you@example.com`, and
`5b10a2844c20165700ede21g`.
