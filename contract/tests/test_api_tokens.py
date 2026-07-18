"""Contract: user API tokens.

Django: apps/api/plane/app/urls/api.py -> ApiTokenEndpoint. Despite living
next to workspace-scoped modules, this endpoint is plain user-scoped:
/api/users/api-tokens/ and /api/users/api-tokens/<uuid:pk>/ -- no workspace
in the path. The APIToken model has a nullable workspace FK, but this view
never sets it, so `workspace` is always null in responses.

Quirks pinned here:
- POST returns 201 (unlike estimate/state's 200 quirk).
- POST/PATCH responses include the raw `token` value and the raw `is_active`
  column (APITokenSerializer). GET (list/retrieve) responses exclude `token`
  and instead compute `is_active` from `expired_at` (APITokenReadSerializer).
- `label` defaults server-side only when the key is *absent* from the body;
  an explicit empty string is kept as-is.
- On PATCH, only `label`/`description` are writable -- token, expired_at,
  workspace, user, is_active, last_used, user_type, allowed_rate_limit are
  read-only and silently ignored even if sent.
- Unknown/nonexistent pk -> 404 {"error": "The required object does not
  exist."}.
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.api_tokens

WRITE_FIELDS = {
    "id": str,
    "created_at": str,
    "updated_at": str,
    "deleted_at": OPTIONAL(str),
    "label": str,
    "description": str,
    "is_active": bool,
    "last_used": OPTIONAL(str),
    "token": str,
    "user_type": int,
    "expired_at": OPTIONAL(str),
    "is_service": bool,
    "allowed_rate_limit": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "user": str,
    "workspace": OPTIONAL(str),
}

READ_FIELDS = {k: v for k, v in WRITE_FIELDS.items() if k != "token"}


def _create(client, **extra):
    body = {"label": "tok-" + unique(), "description": "d"}
    body.update(extra)
    return client.api_post("/api/users/api-tokens/", json=body)


def test_create_api_token(fresh_client):
    r = _create(fresh_client)
    assert_status(r, 201)
    t = r.json()
    assert_has_fields(t, WRITE_FIELDS, where="api_token")
    assert is_uuid(t["id"])
    assert t["user"] == fresh_client.user_id
    assert t["workspace"] is None
    assert t["token"].startswith("plane_api_")
    assert t["user_type"] == 0
    assert t["is_service"] is False
    assert t["allowed_rate_limit"] == "60/min"


def test_create_default_label_when_absent(fresh_client):
    r = fresh_client.api_post("/api/users/api-tokens/", json={})
    assert_status(r, 201)
    t = r.json()
    assert isinstance(t["label"], str) and len(t["label"]) > 0
    assert t["description"] == ""


def test_create_empty_label_kept_as_is(fresh_client):
    r = fresh_client.api_post("/api/users/api-tokens/", json={"label": ""})
    assert_status(r, 201)
    assert r.json()["label"] == ""


def test_create_ignores_readonly_fields(fresh_client):
    r = _create(fresh_client, user_type=1, workspace=fresh_client.user_id, is_active=False)
    assert_status(r, 201)
    t = r.json()
    assert t["user_type"] == 0
    assert t["workspace"] is None
    assert t["is_active"] is True


def test_list_api_tokens(fresh_client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rl = fresh_client.api_get("/api/users/api-tokens/")
    assert_status(rl, 200)
    tokens = rl.json()
    assert isinstance(tokens, list)
    match = next(x for x in tokens if x["id"] == tid)
    assert_has_fields(match, READ_FIELDS, where="api_token(list)")
    assert "token" not in match


def test_list_only_own_tokens(fresh_client, client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rl = client.api_get("/api/users/api-tokens/")
    assert_status(rl, 200)
    assert all(x["id"] != tid for x in rl.json())


def test_retrieve_api_token(fresh_client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rr = fresh_client.api_get(f"/api/users/api-tokens/{tid}/")
    assert_status(rr, 200)
    t = rr.json()
    assert_has_fields(t, READ_FIELDS, where="api_token(retrieve)")
    assert "token" not in t
    assert t["id"] == tid


def test_retrieve_computes_is_active_from_expired_at(fresh_client):
    r = _create(fresh_client, expired_at="2020-01-01T00:00:00Z")
    assert_status(r, 201)
    # create response uses the raw (always-true) column
    assert r.json()["is_active"] is True
    tid = r.json()["id"]
    rr = fresh_client.api_get(f"/api/users/api-tokens/{tid}/")
    assert_status(rr, 200)
    assert rr.json()["is_active"] is False
    assert rr.json()["expired_at"] is not None


def test_retrieve_not_owned_returns_404(fresh_client, client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rr = client.api_get(f"/api/users/api-tokens/{tid}/")
    assert_status(rr, 404)


def test_retrieve_nonexistent_returns_404(fresh_client):
    import uuid

    rr = fresh_client.api_get(f"/api/users/api-tokens/{uuid.uuid4()}/")
    assert_status(rr, 404)


def test_update_label_and_description(fresh_client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rp = fresh_client.api_patch(
        f"/api/users/api-tokens/{tid}/",
        json={"label": "updated-label", "description": "updated-desc"},
    )
    assert_status(rp, 200)
    t = rp.json()
    assert_has_fields(t, WRITE_FIELDS, where="api_token(update)")
    assert t["label"] == "updated-label"
    assert t["description"] == "updated-desc"


def test_update_ignores_readonly_fields(fresh_client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    orig_token = r.json()["token"]
    rp = fresh_client.api_patch(
        f"/api/users/api-tokens/{tid}/",
        json={"token": "hacked", "is_active": False, "user_type": 1, "expired_at": "2099-01-01T00:00:00Z"},
    )
    assert_status(rp, 200)
    t = rp.json()
    assert t["token"] == orig_token
    assert t["is_active"] is True
    assert t["user_type"] == 0
    assert t["expired_at"] is None


def test_update_nonexistent_returns_404(fresh_client):
    import uuid

    rp = fresh_client.api_patch(f"/api/users/api-tokens/{uuid.uuid4()}/", json={"label": "x"})
    assert_status(rp, 404)


def test_delete_api_token(fresh_client):
    r = _create(fresh_client)
    tid = r.json()["id"]
    rd = fresh_client.api_delete(f"/api/users/api-tokens/{tid}/")
    assert_status(rd, 204)
    rr = fresh_client.api_get(f"/api/users/api-tokens/{tid}/")
    assert_status(rr, 404)


def test_delete_nonexistent_returns_404(fresh_client):
    import uuid

    rd = fresh_client.api_delete(f"/api/users/api-tokens/{uuid.uuid4()}/")
    assert_status(rd, 404)


def test_unauthenticated_returns_401():
    import requests
    import os

    base_url = os.environ.get("BASE_URL", "http://localhost:8000").rstrip("/")
    r = requests.get(f"{base_url}/api/users/api-tokens/", timeout=30)
    assert_status(r, 401)
