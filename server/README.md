# plane-go

Go reimplementation of Plane's app API (core PM slice), reusing the existing
frontend and keeping the same HTTP contract. Correctness is defined by the
black-box contract suite in `../api-go-contract/` (frozen against the Python
reference, then repointed here via `BASE_URL`).

Stack: chi (router) · pgx (driver) · sqlc (type-safe queries) · goose (migrations).

## Layout
```
cmd/server/            main: config, pgx pool, chi router
internal/config/       env config
internal/httpx/        JSON + error-envelope helpers
internal/auth/         /auth/* session flow, pbkdf2, csrf, session store, middleware
internal/user/         /api/users/me/
internal/db/migrations goose SQL migrations
internal/db/queries    sqlc query sources
internal/db/gen        sqlc-generated code (do not edit)
```

## Dev
```bash
# DB runs as `plane-go-db` in the repo compose (host :4010)
make tidy         # fetch deps
make generate     # sqlc -> internal/db/gen
make migrate-up   # apply migrations to plane_go
make run          # serve on :4001

# gate: run the contract suite against this server
cd ../api-go-contract && BASE_URL=http://localhost:4001 pytest tests/test_auth.py -v
```

Config (env, all defaulted): `PLANE_GO_ADDR` (:4001), `DATABASE_URL`,
`SESSION_COOKIE_NAME` (session-id), `WEB_URL` (http://localhost:3000).
