BASE_URL ?= http://localhost:8000
COMPOSE_DIR := ../..

.PHONY: install contract contract-python contract-go reset-throttle
install:
	pip install -r requirements.txt

# Run the whole suite against whatever BASE_URL points at.
contract:
	BASE_URL=$(BASE_URL) pytest -v

# Reference-side only: the dockerized Python api throttles anon requests at
# 30/min and the whole suite runs in ~1s, so counters from a prior run can
# linger. Flush the redis-backed throttle counters first. Best-effort (leading
# '-'): a no-op when redis isn't the local docker service. Sessions are
# DB-backed, so this does not affect auth state.
reset-throttle:
	-cd $(COMPOSE_DIR) && docker compose exec -T plane-redis redis-cli FLUSHDB >/dev/null 2>&1

contract-python: reset-throttle
	BASE_URL=http://localhost:8000 pytest -v

# Go port has no such throttle; just run the suite.
contract-go:
	BASE_URL=$${BASE_URL:-http://localhost:8080} pytest -v
