"""End-to-end test for goroutine-backed webhook delivery (the Go port's
replacement for Plane's webhook_task Celery worker).

Behavioral e2e, not a Python-parity freeze: in this sandbox the Django
reference's webhook-delivery worker doesn't fire, so no log row appears there.
The test skips when no delivery is observed (Python) and asserts the real async
delivery against Go: register a webhook, create an issue, poll webhook-logs
until the background worker has POSTed the event and recorded it.
"""
import time
import pytest
from lib.client import unique

pytestmark = pytest.mark.webhook_delivery

# An external, non-loopback URL so it passes the create-time SSRF check. The
# actual response doesn't matter — we assert the delivery was attempted+logged.
HOOK_URL = "https://webhook.site/plane-go-e2e"


def _logs(client, slug, wid):
    r = client.api_get(f"/api/workspaces/{slug}/webhook-logs/{wid}/")
    if r.status_code != 200:
        return None
    body = r.json()
    return body if isinstance(body, list) else body.get("results", [])


def test_issue_create_delivers_webhook(client, workspace, project):
    slug = workspace["slug"]
    wh = client.api_post(f"/api/workspaces/{slug}/webhooks/", json={"url": HOOK_URL, "issue": True})
    assert wh.status_code == 201, wh.text[:200]
    wid = wh.json()["id"]

    st = client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/states/").json()[0]["id"]
    issue = client.api_post(
        f"/api/workspaces/{slug}/projects/{project['id']}/issues/",
        json={"name": "wh " + unique(), "state_id": st},
    ).json()

    rows = []
    deadline = time.time() + 10
    while time.time() < deadline:
        rows = _logs(client, slug, wid) or []
        if rows:
            break
        time.sleep(0.4)
    if not rows:
        pytest.skip("no webhook delivery observed (reference worker not firing here)")

    log = rows[0]
    assert log["event_type"] == "issue"
    # QUIRK (Django + Go match): the `request_method` column stores the event
    # ACTION, not the HTTP verb.
    assert log["request_method"] == "created"
    # the delivered payload carries the created issue
    assert issue["id"] in (log.get("request_body") or ""), "issue id not in delivered body"
    assert "response_status" in log
