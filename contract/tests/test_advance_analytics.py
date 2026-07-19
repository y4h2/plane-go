"""Contract: the workspace Analytics page (advance-analytics). Overview stat
tiles, per-project work-item stats, and the projects/work-items charts. Go was
returning 404 for all of these, leaving the Analytics page showing zeros.
"""
import pytest
from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.advance_analytics


def W(ws):
    return f"/api/workspaces/{ws['slug']}"


def B(ws, p):
    return f"/api/workspaces/{ws['slug']}/projects/{p['id']}"


def _seed(client, ws, p, n):
    st = client.api_get(B(ws, p) + "/states/").json()[0]["id"]
    return [client.api_post(B(ws, p) + "/issues/", json={"name": "AA " + unique(), "state_id": st}).json()["id"] for _ in range(n)]


def test_advance_overview_counts(client, workspace, project):
    _seed(client, workspace, project, 3)
    r = client.api_get(W(workspace) + "/advance-analytics?tab=overview")
    assert_status(r, 200)
    body = r.json()
    for k in ("total_users", "total_admins", "total_members", "total_guests",
              "total_projects", "total_work_items", "total_cycles", "total_intake"):
        assert k in body, f"missing {k}"
        assert isinstance(body[k]["count"], int)
    assert body["total_projects"]["count"] >= 1
    assert body["total_work_items"]["count"] >= 3


def test_advance_charts_projects(client, workspace, project):
    r = client.api_get(W(workspace) + "/advance-analytics-charts?type=projects")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list) and body
    keys = {row["key"] for row in body}
    assert {"work_items", "cycles", "modules", "members"} <= keys
    for row in body:
        assert isinstance(row["count"], int) and "name" in row


def test_advance_charts_work_items_timeseries(client, workspace, project):
    _seed(client, workspace, project, 2)
    r = client.api_get(W(workspace) + "/advance-analytics-charts?type=work-items&group_by=priority")
    assert_status(r, 200)
    body = r.json()
    assert "data" in body and "schema" in body
    assert isinstance(body["data"], list)
    for row in body["data"]:
        for k in ("key", "count", "completed_issues", "created_issues"):
            assert k in row


def test_advance_stats_work_items(client, workspace, project):
    _seed(client, workspace, project, 2)
    r = client.api_get(W(workspace) + "/advance-analytics-stats?tab=work-items")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list) and body
    for row in body:
        for k in ("project_id", "project__name", "cancelled_work_items",
                  "completed_work_items", "backlog_work_items",
                  "un_started_work_items", "started_work_items"):
            assert k in row
