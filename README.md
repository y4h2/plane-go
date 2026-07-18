# plane-go

A **Go reimplementation of [Plane](https://github.com/makeplane/plane)'s backend HTTP API**
(the "app API" the web frontend talks to). It reuses Plane's existing frontend unchanged and
keeps the same HTTP contract, so it's a drop-in replacement for the covered surface — validated
by a language-agnostic black-box contract suite that passes identically against this Go server
**and** against the upstream Python/Django backend.

- **Server:** `server/` — chi (router) · pgx (driver) · sqlc (type-safe queries) · goose (migrations)
- **Contract suite:** `contract/` — ~130 black-box HTTP tests (pytest + requests) across 18 modules
- **Parity:** the same suite is green against the Go server (:4001) and the reference Python API

## Why

Plane's own test suite is white-box (it calls the API in-process and asserts through Django's
ORM), so it can't gate a rewrite in another language. This project instead defines correctness
as a **black-box HTTP contract**: set up state via the API, assert only on responses. Freeze that
suite green against the Python reference, then make the Go server pass the identical suite.

## What's covered

Auth (session cookie, PBKDF2, CSRF) and the core project-management surface:

- **Core entities:** workspace, project (+ members, invitations), issue, state, label, cycle, module
- **Issue detail:** comments, links, sub-issues, subscribe/reactions
- **Views & prefs:** saved views (workspace + project), project user-properties, estimates
- **Boot/identity:** user settings/profile, instance config, workspace members, project-roles
- **Workspace extras:** notifications, favorites, home-page reads (recent-visits, quick-links, stickies)

~62 route patterns, 14 migrations. Not yet covered (niche): attachments (S3), analytics,
importers, webhooks, enterprise/license.

## Run it

```bash
# 1. Start Postgres (host :4010)
docker compose up -d db

# 2. Migrate + run the Go server (needs Go 1.26+, goose, sqlc)
cd server
make migrate-up          # goose -dir internal/db/migrations postgres "$DATABASE_URL" up
make run                 # serves on :4001

# 3. Run the contract suite against it (in another shell)
cd contract
python -m venv .venv && . .venv/bin/activate && pip install -r requirements.txt
BASE_URL=http://localhost:4001 make contract
```

Config (env, all defaulted): `PLANE_GO_ADDR` (`:4001`), `DATABASE_URL`
(`postgres://plane:plane@localhost:4010/plane_go?sslmode=disable`),
`SESSION_COOKIE_NAME` (`session-id`), `WEB_URL` (`http://localhost:3000`).

## Provenance & license

This is an independent reimplementation of Plane's HTTP API, developed by observing the API's
observable behavior (the black-box contract suite in `contract/`). Because it derives from
studying Plane — which is licensed **AGPL-3.0-only** — this repository carries the same license
(see [`LICENSE`](./LICENSE)). Plane is a trademark of its authors; this project is not affiliated
with or endorsed by them.
