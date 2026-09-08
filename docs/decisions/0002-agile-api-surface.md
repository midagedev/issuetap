# 0002 — Agile API surface

Serving `/rest/agile/1.0` (boards, sprints, backlog moves) plus the
`gh-sprint` custom field and JQL `sprint` functions is Jira API
compatibility, not a planning feature: gadak's first-class sprints
(gadak GDK-1666) talk to exactly that REST surface and read exactly that
field, so an issuetap origin that omits it forces every sprint test
through a second fake. "Issuetap is not an issue tracker" bars features
a tracker user would want — ranking, reports, a board UI — and this is
not one of them: there is no web surface, no board layout, no planning
semantics beyond the two state transitions Jira's own API defines
(start requires dates, close sweeps incomplete issues to the backlog).
What this decision does not license: fixture/snapshot authoring of
sprints (boards are created lazily by the API, one scrum board per
project) or any `web/` change.
