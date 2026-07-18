"""Contract: workspace webhooks (CRUD + secret regeneration + webhook-logs).

Quirks discovered by probing the Python reference directly:

- The list/retrieve/patch views pass a `fields=(...)` restriction into
  `WebhookSerializer`, but `DynamicBaseSerializer.__init__` unconditionally
  overwrites that kwarg with `self.expand` (a separate, unrelated kwarg that
  defaults to `[]`). The restriction is therefore dead code: every response
  (create/list/retrieve/patch/regenerate) returns *all* model fields,
  including `secret_key`, `workspace`, `created_by`, `updated_by`,
  `deleted_at`, `is_internal`, and `version` — even in the list endpoint.
- Create returns 201 (unlike estimate/state's 200 quirk).
- Webhook URLs go through a real SSRF check (DNS resolution + private/loopback
  block), so `http://localhost/...` and unresolvable hosts (e.g.
  `https://example.com/...`, blocked by DNS in this environment) are both
  rejected. `https://webhook.site/<path>` resolves publicly and is used as the
  valid-URL fixture throughout.
- Duplicate URL within a workspace -> 409 (IntegrityError caught explicitly),
  not a plain 400.
- A bad/absent workspace slug -> 403 "You don't have the required
  permissions." (the WORKSPACE-level admin-role permission check runs before
  any workspace lookup, so a nonexistent workspace looks like "no role
  there"), not 404.
- Regenerate is a POST with no meaningful body; it always issues a fresh
  secret_key and bumps updated_at/updated_by regardless of request payload.
"""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.webhooks

WEBHOOK_FIELDS = {
    "id": str,
    "url": str,
    "is_active": bool,
    "secret_key": str,
    "project": bool,
    "issue": bool,
    "module": bool,
    "cycle": bool,
    "issue_comment": bool,
    "is_internal": bool,
    "version": str,
    "created_at": str,
    "updated_at": str,
    "workspace": str,
}


def _webhook_url() -> str:
    return "https://webhook.site/" + unique()


def _create(client, workspace, **extra):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    payload = {"url": _webhook_url(), **extra}
    r = client.api_post(base, json=payload)
    return base, r


def test_create_webhook(client, workspace):
    base, r = _create(client, workspace)
    assert_status(r, 201)
    wh = r.json()
    assert_has_fields(wh, WEBHOOK_FIELDS, where="webhook")
    assert is_uuid(wh["id"])
    assert wh["is_active"] is True
    assert wh["secret_key"].startswith("plane_wh_")
    assert wh["workspace"] == workspace["id"]
    for flag in ("project", "issue", "module", "cycle", "issue_comment", "is_internal"):
        assert wh[flag] is False, flag
    assert wh["version"] == "v1"


def test_create_webhook_with_flags(client, workspace):
    base, r = _create(
        client,
        workspace,
        project=True,
        issue=True,
        cycle=True,
        module=True,
        issue_comment=True,
        is_active=False,
        is_internal=True,
        version="v2",
    )
    assert_status(r, 201)
    wh = r.json()
    for flag in ("project", "issue", "cycle", "module", "issue_comment", "is_internal"):
        assert wh[flag] is True, flag
    assert wh["is_active"] is False
    assert wh["version"] == "v2"


def test_list_webhooks_includes_secret(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rl = client.api_get(base)
    assert_status(rl, 200)
    items = rl.json()
    assert isinstance(items, list)
    match = next((w for w in items if w["id"] == wid), None)
    assert match is not None, f"webhook {wid} missing from list"
    # Quirk: list also exposes the full field set, including secret_key.
    assert_has_fields(match, WEBHOOK_FIELDS, where="webhook.list_item")


def test_retrieve_webhook(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rr = client.api_get(base + f"{wid}/")
    assert_status(rr, 200)
    wh = rr.json()
    assert_has_fields(wh, WEBHOOK_FIELDS, where="webhook")
    assert wh["id"] == wid
    assert wh["secret_key"] == r.json()["secret_key"]


def test_update_webhook(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rp = client.api_patch(base + f"{wid}/", json={"is_active": False, "project": True})
    assert_status(rp, 200)
    wh = rp.json()
    assert wh["is_active"] is False
    assert wh["project"] is True
    # unrelated flags untouched
    assert wh["issue"] is False
    # secret is not mutated by a plain update
    assert wh["secret_key"] == r.json()["secret_key"]


def test_update_webhook_url_validated(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rp = client.api_patch(base + f"{wid}/", json={"url": "http://localhost/x"})
    assert_status(rp, 400)
    body = rp.json()
    assert "url" in body


def test_regenerate_secret(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    old_secret = r.json()["secret_key"]
    rr = client.api_post(base + f"{wid}/regenerate/", json={})
    assert_status(rr, 200)
    wh = rr.json()
    assert_has_fields(wh, WEBHOOK_FIELDS, where="webhook")
    assert wh["id"] == wid
    assert wh["secret_key"].startswith("plane_wh_")
    assert wh["secret_key"] != old_secret
    # everything else preserved
    assert wh["url"] == r.json()["url"]


def test_delete_webhook(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rd = client.api_delete(base + f"{wid}/")
    assert_status(rd, 204)
    rg = client.api_get(base + f"{wid}/")
    assert_status(rg, 404)


def test_duplicate_url_conflict(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    url = _webhook_url()
    r1 = client.api_post(base, json={"url": url})
    assert_status(r1, 201)
    r2 = client.api_post(base, json={"url": url})
    assert_status(r2, 409)
    assert "error" in r2.json()


def test_invalid_url_rejected(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    r = client.api_post(base, json={"url": "not-a-url"})
    assert_status(r, 400)
    body = r.json()
    assert "url" in body
    assert isinstance(body["url"], list)


def test_localhost_url_rejected(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    r = client.api_post(base, json={"url": "http://localhost/hook"})
    assert_status(r, 400)
    assert "url" in r.json()


def test_missing_url_rejected(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    r = client.api_post(base, json={})
    assert_status(r, 400)
    assert "url" in r.json()


def test_get_nonexistent_webhook(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_get(base + f"{fake}/")
    assert_status(r, 404)
    assert "error" in r.json()


def test_patch_nonexistent_webhook(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_patch(base + f"{fake}/", json={"is_active": False})
    assert_status(r, 404)


def test_delete_nonexistent_webhook(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_delete(base + f"{fake}/")
    assert_status(r, 404)


def test_regenerate_nonexistent_webhook(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/webhooks/"
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_post(base + f"{fake}/regenerate/", json={})
    assert_status(r, 404)


def test_invalid_workspace_slug(client):
    r = client.api_get("/api/workspaces/does-not-exist-workspace-slug/webhooks/")
    assert_status(r, 403)
    assert "error" in r.json()


def test_unauthenticated_rejected(base_url):
    import requests

    r = requests.get(f"{base_url}/api/workspaces/anything/webhooks/", timeout=30)
    assert_status(r, 401)


def test_webhook_logs_list(client, workspace):
    base, r = _create(client, workspace)
    wid = r.json()["id"]
    rl = client.api_get(f"/api/workspaces/{workspace['slug']}/webhook-logs/{wid}/")
    assert_status(rl, 200)
    assert isinstance(rl.json(), list)
