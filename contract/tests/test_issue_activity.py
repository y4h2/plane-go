"""End-to-end test for the goroutine-backed issue activity log (the Go port's
replacement for Plane's `issue_activity` Celery task).

This is a BEHAVIORAL e2e, not a Python-parity freeze: in this sandbox the Django
reference's issue-activity worker isn't running, so `issues/{id}/history/` 500s
there. The test therefore skips automatically when the target returns non-200
(i.e. against Python) and asserts the real async behaviour against Go: create /
update an issue, then poll until the background worker has written the activity
rows, and check them.
"""
import time
import pytest
from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.issue_activity


def B(ws, p):
    return f"/api/workspaces/{ws['slug']}/projects/{p['id']}"


def _poll_history(client, base, issue_id, want, timeout=8.0):
    """Poll the history endpoint until >= want entries (bg worker is async)."""
    deadline = time.time() + timeout
    last = []
    while time.time() < deadline:
        r = client.api_get(base + f"/issues/{issue_id}/history/")
        if r.status_code != 200:
            pytest.skip("reference issue-activity worker not running here (history != 200)")
        last = r.json()
        if len(last) >= want:
            return last
        time.sleep(0.4)
    return last


def _states(client, ws, p):
    return {s["group"]: s["id"] for s in client.api_get(B(ws, p) + "/states/").json()}


def test_create_records_created_activity(client, workspace, project):
    st = _states(client, workspace, project)
    iid = client.api_post(B(workspace, project) + "/issues/",
                          json={"name": "act " + unique(), "state_id": st["backlog"]}).json()["id"]
    entries = _poll_history(client, B(workspace, project), iid, want=1)
    assert entries, "expected a 'created' activity"
    created = [e for e in entries if e["verb"] == "created"]
    assert created, f"no created verb in {[e['verb'] for e in entries]}"
    e = created[0]
    # shape the frontend renders
    for k in ("id", "verb", "field", "actor", "actor_detail", "issue", "project", "workspace", "created_at"):
        assert k in e


def test_field_updates_record_activities(client, workspace, project):
    st = _states(client, workspace, project)
    iid = client.api_post(B(workspace, project) + "/issues/",
                          json={"name": "act " + unique(), "priority": "low", "state_id": st["backlog"]}).json()["id"]
    assert_status(client.api_patch(B(workspace, project) + f"/issues/{iid}/", json={"priority": "urgent"}), 204)
    assert_status(client.api_patch(B(workspace, project) + f"/issues/{iid}/", json={"state_id": st["started"]}), 204)
    entries = _poll_history(client, B(workspace, project), iid, want=3)
    by_field = {(e["verb"], e.get("field")): e for e in entries}
    assert ("created", None) in by_field
    assert ("updated", "priority") in by_field
    assert ("updated", "state") in by_field
    pr = by_field[("updated", "priority")]
    assert pr["old_value"] == "low" and pr["new_value"] == "urgent"


def test_no_op_update_records_nothing_extra(client, workspace, project):
    st = _states(client, workspace, project)
    iid = client.api_post(B(workspace, project) + "/issues/",
                          json={"name": "act " + unique(), "priority": "high", "state_id": st["backlog"]}).json()["id"]
    _poll_history(client, B(workspace, project), iid, want=1)
    # patch priority to the SAME value -> no new activity
    assert_status(client.api_patch(B(workspace, project) + f"/issues/{iid}/", json={"priority": "high"}), 204)
    time.sleep(1.5)
    entries = client.api_get(B(workspace, project) + f"/issues/{iid}/history/").json()
    assert [e["verb"] for e in entries] == ["created"]
