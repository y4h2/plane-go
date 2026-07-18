# Plane API — black-box contract suite

Language-agnostic HTTP contract tests for Plane's **app API** (`/api/*`, `/auth/*`).
They talk to a running server over HTTP only — set up state via the API, assert on
responses — so the **same suite** validates the Python reference and the Go port.

## Why this exists

The repo's Django `contract/` tests are white-box: they call the API in-process and
assert through the Django ORM, so they can't gate a Go rewrite. This suite is the
cross-language gate: freeze it green against Python, then repoint it at Go.

## Running

```bash
# against the Python reference (published on :8000 via docker-compose.override.yml)
make contract-python          # BASE_URL=http://localhost:8000

# against the Go port
BASE_URL=http://localhost:8080 make contract-go
```

Or directly:
```bash
python -m venv .venv && . .venv/bin/activate && pip install -r requirements.txt
BASE_URL=http://localhost:8000 pytest -v
```

## Design

- `lib/client.py` — `PlaneClient`, a `requests.Session` wrapper implementing the real
  auth flow (get-csrf → sign-up/sign-in → `session-id` cookie) and `api_*` helpers.
- `lib/shape.py` — structural assertions: status codes, pagination-envelope shape,
  and field presence/type (dynamic values like ids/timestamps are type-checked, not
  value-matched), so the contract is pinned without hard-coding volatile data.
- `conftest.py` — `client` (signed-in), `workspace`, `project` fixtures, all via the API.
- `tests/test_<entity>.py` — one module per core entity.

Scope: auth, workspace, project, project-members, state, label, issue, cycle, module.
