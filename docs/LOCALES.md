# Locales

Atlassian localizes display names per account. A Korean-language site
returns `진행 중` rather than `In Progress`, `작업` rather than `Task`.
Every client that keys logic on a display name silently returns zero rows.

`issuetap serve --locale ko` (also `ja`, `de`, `en`) serves the **same
ids** with localized status, priority, and issue-type names.
`statusCategory.key` stays `new` / `indeterminate` / `done`.

## What a live ko_KR Cloud site actually returned (2026-08-15)

GET only, redacted. This is the most useful thing this build learned:

| Catalog | Localized on that site? | Examples |
| --- | --- | --- |
| Status `name` | yes | `해야 할 일`, `진행 중`, `검토 중`, `완료`; some next-gen names stayed English (`Backlog`) |
| Status `untranslatedName` | mixed | system statuses often keep the English untranslated name; site-created ones stay Korean |
| `statusCategory.key` | **no** (stable) | `new`, `indeterminate`, `done` |
| `statusCategory.name` | yes | `해야 할 일`, `진행 중`, `완료` |
| `statusCategory.id` | n/a | 2 = new, 4 = indeterminate, 3 = done |
| Priority `name` | **no** | `Highest` / `High` / `Medium` / `Low` / `Lowest` even on ko_KR |
| Issue type `name` | yes | `작업`, `버그`, `스토리`, `에픽`, `하위 작업` |
| Field catalog `name` | yes | `이슈 유형`, `담당자`, `우선 순위`, `상태` |
| Two statuses, one name | yes | two different ids both named `진행 중`; two both named `완료` |
| Two types, one name | yes | two `작업`, two `에픽`, two `버그` |

The PRD assumed priorities would localize. On this site they did not.
Issuetap still localizes priorities under `--locale ko` so a client that
keys on `"High"` fails here even when a particular real site would not.
That is the product: make the trap fail loudly. `examples/fixtures/korean.yaml`
uses Korean priority names for the same reason.

`--type Task` against a Korean site fails because the type is called `작업`.
Key on `issue_type_id` or `issuetype = 10003`.

## How to write a client that survives this

- Status: `statusCategory.key` or `status.id`. Never `status.name`.
- Priority: `priority.id` (or a rank derived from `GET /priority` order).
  Never `priority.name`.
- Issue type: `issuetype.id`. Never `issuetype.name`.
- Changelog: `items[].fieldId`. `items[].field` is localized (`상태`,
  `담당자`, `우선 순위`).

## Scenario

`examples/scenarios/locale-ko-name-trap.yaml` asserts:

- `GET /status` contains `진행 중`
- `status = "In Progress"` returns zero issues
- `status = 3` returns TAP-1
