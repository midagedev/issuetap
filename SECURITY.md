# Security Policy

Issuetap is a local testbed. No supported version line exists yet.

## Reporting A Vulnerability

Open a private security advisory when available:

```text
https://github.com/midagedev/issuetap/security/advisories/new
```

If advisories are not configured, contact the maintainer through the
repository owner profile.

Do not include live Atlassian credentials, site hosts, or issue data from
a real site.

## Product Boundary

Issuetap must not be treated as a Jira. See
`docs/PRODUCTION_BOUNDARIES.md`.

## Secret Handling

- Never commit `ISSUETAP_REF_SITE`, `ISSUETAP_REF_EMAIL`, or
  `ISSUETAP_REF_TOKEN`.
- Fixtures use `example.atlassian.net`, `you@example.com`, and
  `5b10a2844c20165700ede21g`.
- `make secretscan` greps for token shapes and non-example hosts.
- Request traces do not record the Authorization header.
