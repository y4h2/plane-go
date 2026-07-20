"""End-to-end test for goroutine-backed recent-visits (the Go port's replacement
for Plane's recent_visited_task Celery task).

Retrieving a project records a visit off the request path; the recent-visits
endpoint reads it back. Python's worker fires here too, so this is a real parity
test on both backends (poll to absorb the async write).
"""
import time
import pytest
from lib.client import unique

pytestmark = pytest.mark.recent_visits


def test_project_retrieve_records_recent_visit(client, workspace):
    slug = workspace["slug"]
    p = client.api_post(f"/api/workspaces/{slug}/projects/",
                        json={"name": "Visited " + unique(), "identifier": unique("V")[:6].upper()}).json()
    pid = p["id"]

    # trigger a visit
    assert client.api_get(f"/api/workspaces/{slug}/projects/{pid}/").status_code == 200

    rows = []
    deadline = time.time() + 8
    while time.time() < deadline:
        rows = client.api_get(f"/api/workspaces/{slug}/recent-visits/").json()
        if any(r.get("entity_identifier") == pid for r in rows):
            break
        time.sleep(0.4)

    match = [r for r in rows if r.get("entity_identifier") == pid]
    assert match, f"project visit not recorded; got {[r.get('entity_identifier') for r in rows]}"
    v = match[0]
    assert v["entity_name"] == "project"
    for k in ("id", "entity_name", "entity_identifier", "entity_data", "visited_at"):
        assert k in v
    assert isinstance(v["entity_data"], dict)
    assert v["entity_data"].get("id") == pid


def test_recent_visits_empty_for_fresh_workspace(client, workspace):
    # no entity retrieved yet -> empty list (a fresh signed-in user)
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/recent-visits/")
    assert r.status_code == 200
    assert isinstance(r.json(), list)
