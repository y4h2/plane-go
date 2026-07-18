# plane-go — Status & Coverage Tracker

Living status of the Go reimplementation of Plane's **app API** (the surface the
web frontend uses). Update the tables below whenever a batch lands.

_Last updated: 2026-07-18_

## Snapshot

| Metric | Value |
|---|---|
| Go route patterns implemented | ~165 distinct paths |
| Django app-API endpoints (total) | 233 |
| Contract tests (black-box, Go↔Python parity) | **357, all green on BOTH Go and Python** |
| Migrations | 0001–0026 |
| Full app runs against Go in a browser | ✅ (signup → onboarding → projects → issues → cycles) |

**Run it:** `docker compose up -d db` → `cd apps/api-go && make migrate-up && WEB_URL=http://localhost make run` (`:4001`).
Frontend against Go: point the Caddy proxy `/api`+`/auth` at the host `:4001` (see `docker-compose.override.yml`), open `http://localhost`.
Demo login: `demo@planego.test` / `qK7#mvZ2rLpX9w`.

## Endpoint coverage by module

Status: ✅ complete · 🟡 core done, secondary actions missing · ❌ not started

| Module | Django eps | Status | Implemented | Missing (notable) |
|---|---:|:--:|---|---|
| state | 4 | ✅ | CRUD, mark-default, intake-state | — |
| timezone | 1 | ✅ | list | — |
| user | 16 | 🟡 | me, settings, profile (get/patch), onboard, tour-completed, workspaces, ws-invitations, change-password (/auth, +CSRF), deactivate (DELETE /users/me/) | accounts (OAuth), activity feed |
| workspace | 41 | 🟡 | CRUD, members, members/me, invitations (list/create/accept), project-roles, slug-check, sidebar/home/user prefs, recent-visits, quick-links, stickies, favorites, notifications (list+unread), estimates-list | ws views, analytics, exports, entity-search, activity |
| project | 20 | 🟡 | CRUD, details, members CRUD+role, members/me, project-roles, identifiers, favorites, cover image, archive/unarchive (+404 on archived detail), project-stats | search-issues, features |
| issue | 40 | 🟡 | CRUD, list (envelope+group_by), list-by-ids, comments, links, sub-issues (r/w), subscribers, subscribe, reactions, meta, relations (r/w + inverse), attachments (list), archive/unarchive, bulk-delete, bulk-archive, history (stub) | real activity feed, drafts, issue-dates, deleted-issues, versions, work-item identifier lookup |
| cycle | 14 | ✅ | CRUD, cycle-issues, favorites, transfer-issues, date-check, progress, analytics (burndown), archive/unarchive + archived-cycles | — |
| module | 13 | ✅ | CRUD, module-issues, favorites, links, archive/unarchive + archived-modules, embedded progress counts + distribution | — |
| views | 7 | ✅ | CRUD (workspace + project), favorite-views, workspace-level issue list | — |
| issue-extras | — | ✅ | draft-issues CRUD + draft-to-issue, bulk issue-dates; issue start/target dates now surfaced | — |
| estimate | 5 | ✅ | create, list, retrieve, update (400 without points), delete | — |
| asset | 18 | 🟡 | v2 create/upload/patch/bulk/serve (self-hosted local store), project cover | restore, duplicate, legacy file-assets |
| notification | 7 | 🟡 | list, unread-count, paginated list, mark-all-read, per-id read/archive (stubs — no notification generation) | snooze, real generation |
| instance | (auth) | ✅ | GET /instances/ (public) | — |
| search | 3 | ✅ | global search, entity (@mention) search, project-stats (fields/project_ids) | — |
| analytic | 13 | 🟡 | analytics (distribution+extras), default-analytics | advance-analytics (needs demo-seed), analytic-view CRUD, export |
| page | 11 | 🟡 | CRUD, summary, archive/unarchive, lock/unlock, access, favorite, duplicate, destroy | description/versions (need live collab server) |
| intake | 10 | ✅ | intake CRUD, intake-issues CRUD, status transitions (accept/reject/snooze/duplicate), lazy default-intake | — |
| webhook | 4 | ✅ | CRUD, regenerate secret, webhook-logs | — |
| external | 3 | ✅ | unsplash + AI-assistant (unconfigured-key parity) | live 3rd-party calls (no keys in env) |
| api (tokens) | 2 | ✅ | token CRUD (list/create/retrieve/patch/delete) | — |
| exporter | 1 | ✅ | create export job, list jobs | actual celery processing (no worker in Go) |

## Auth (/auth/*) — ✅ complete

Session cookie (`session-id`, DB-backed), pbkdf2 (Django format), CSRF double-submit,
sign-up/in/out, get-csrf-token, email-check.

## Changelog (batches)

- **Core slice** — auth + workspace, project(+members/invites), issue, state, label, cycle, module. 97 contract tests frozen on Python → green on Go.
- **Expansion 1** — identity/boot (settings, profile, instances, ws-members, project-roles), issue detail (comments/links/sub-issues), issue social (subscribe/subscribers/reactions), views, user-properties, estimate, notifications+favorites, cycle/module favorites+transfer, workspace-home. → 131 tests.
- **Run-it fixes** — email-check, PATCH me/profile/onboard, instances made public; self-hosted **asset upload store** (fixes cover images / uploads) + project cover.
- **UI walk fixes** — sidebar-preferences, workspace user-properties, intake-state, timezones, GET invitations, workspace estimates, paginated notifications; issue meta/issue-relation/history/description-versions; **tour-completed** (welcome-modal dismissal).
- **Issue actions** — archive/unarchive (state-guarded), bulk-delete, bulk-archive, sub-issues write, issue-relation write/remove (+inverse), attachments list. Migration 0017.
- **Cycle progress/analytics + notification actions** — cycle `progress/` (issue counts by state group), `analytics` (burndown completion_chart), `cycle-progress/` alias; notification `mark-all-read` + per-id read/archive stubs.
- **Search + estimate/project round-out + asset fixes** — global search, entity(@mention) search, project-stats (with `fields`/`project_ids` selection); estimate update/delete (PATCH 400 without points); project archive/unarchive (+404 on archived detail); asset entity_type validation (400), PATCH-confirm→204 + 404 on missing; issue `remove-relation` POST route. + 5 backfill test modules for the run-it/UI-walk/issue-action batches.
- **Remaining-module wave (7 parallel agents, each with e2e tests frozen on Python)** — **api-tokens** (CRUD, mig 0021), **webhooks** (CRUD + regenerate + logs, mig 0020), **intake/triage** (CRUD + status transitions, lazy default-intake, mig 0019), **pages** (metadata CRUD/archive/lock/favorite/duplicate, mig 0022; live-sync endpoints skipped), **analytics** (analytics + default-analytics), **exporter** (job create/list, mig 0023) + **external** (unsplash/AI unconfigured-key parity). → **275 tests, green on both servers.**
- **Secondary-actions wave (4 parallel agents)** — **cycle+module archive/unarchive** + archived lists (mig 0024; added `archived_at` to cycles/modules, filtered their normal lists), **view favorites + workspace-level issue list** (mig 0025, reuses user_favorites), **draft-issues CRUD + bulk issue-dates** (mig 0026), **user change-password (/auth, +CSRF) + deactivate**. Fixed the shared issue `.values()` to surface real `start_date`/`target_date` (was hardcoded null). → **336 tests, green on both servers.**
- **Tail wave (2 agents)** — **deleted-issues** list + **work-item-by-identifier** lookup (`/work-items/{IDENT}-{seq}/`); **module analytics** — modules expose progress counts + distribution embedded in the retrieve/list response (no separate endpoint in Django), wired into `internal/module`'s `.values()`. Integration fixes: issue create now accepts `state_id` (not just `state`); dropped the inert creator auto-subscribe so the identifier endpoint's `is_subscribed` matches Python (false until explicit subscribe). → **357 tests, green on both servers.**

## Remaining work — suggested order

The frontend-facing app-API surface is now covered end-to-end. What's left is deliberately out of scope (needs infra the Go port doesn't run) or low-value:

1. **Pages live collaboration** — binary document sync + version history (`/description/`, `/versions/`). Needs the `live` container; metadata CRUD is done.
2. **Advance-analytics** — the heavier workspace-wide dashboards that depend on demo-seed data / tables not in the Go schema. Basic analytics + default-analytics done.
3. **Real async processing** — exporter celery worker, webhook delivery, notification generation. The HTTP surfaces (create job, register webhook, list notifications) are done; the background workers are not modeled.
4. **Live 3rd-party integrations** — Unsplash/AI actually calling out (no API keys in this env); unconfigured-key behavior matches Python.

## Known limitations

- **Activity feed** returns empty (`history/` stub) — Plane's activity log isn't modeled; it 500s on the Python reference too.
- **Uploads** use a local-disk asset store (not S3/MinIO); files live under `PLANE_GO_ASSET_DIR` (default `/tmp/plane-go-assets`).
- **live** container (collaborative docs/pages) is out of scope; the Pages feature is unimplemented.
- New endpoints from the "run-it"/UI-walk/issue-action batches are **not yet in the contract suite** — they're verified manually + by API probes. Backfilling contract tests for them is pending.
