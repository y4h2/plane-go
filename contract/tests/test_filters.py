"""Contract: the project issue list honors filter query params (priority, state,
state_group, created_by). Regression guard — the Go port previously ignored all
filters, so saved views and board filters returned everything.
"""
import pytest
from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.filters


def B(ws, p):
    return f"/api/workspaces/{ws['slug']}/projects/{p['id']}"


def _ids(body):
    """Flatten an issue-list envelope (grouped or flat) to a set of issue ids."""
    res = body["results"]
    if isinstance(res, dict):
        out = []
        for grp in res.values():
            rows = grp["results"] if isinstance(grp, dict) else grp
            out += [r["id"] for r in rows]
        return set(out)
    return {r["id"] for r in res}


def _states(client, ws, p):
    r = client.api_get(B(ws, p) + "/states/").json()
    return {s["group"]: s["id"] for s in r}


def _mk(client, ws, p, priority, state_id):
    return client.api_post(
        B(ws, p) + "/issues/", json={"name": "F " + unique(), "priority": priority, "state_id": state_id}
    ).json()["id"]


def test_filter_by_priority(client, workspace, project):
    st = _states(client, workspace, project)
    urgent = _mk(client, workspace, project, "urgent", st["backlog"])
    low = _mk(client, workspace, project, "low", st["backlog"])
    got = _ids(client.api_get(B(workspace, project) + "/issues/?priority=urgent").json())
    assert urgent in got and low not in got


def test_filter_by_priority_multi(client, workspace, project):
    st = _states(client, workspace, project)
    urgent = _mk(client, workspace, project, "urgent", st["backlog"])
    high = _mk(client, workspace, project, "high", st["backlog"])
    low = _mk(client, workspace, project, "low", st["backlog"])
    got = _ids(client.api_get(B(workspace, project) + "/issues/?priority=urgent,high").json())
    assert {urgent, high} <= got and low not in got


def test_filter_by_state(client, workspace, project):
    st = _states(client, workspace, project)
    a = _mk(client, workspace, project, "none", st["backlog"])
    b = _mk(client, workspace, project, "none", st["started"])
    got = _ids(client.api_get(B(workspace, project) + f"/issues/?state={st['started']}").json())
    assert b in got and a not in got


def test_filter_by_state_group(client, workspace, project):
    st = _states(client, workspace, project)
    backlog = _mk(client, workspace, project, "none", st["backlog"])
    started = _mk(client, workspace, project, "none", st["started"])
    got = _ids(client.api_get(B(workspace, project) + "/issues/?state_group=started").json())
    assert started in got and backlog not in got


def test_filter_by_json_blob_priority(client, workspace, project):
    # the project board sends filters as a JSON blob with Django-style keys,
    # e.g. filters={"priority__in":"urgent,high"} — must filter the same way.
    import json
    from urllib.parse import quote
    st = _states(client, workspace, project)
    urgent = _mk(client, workspace, project, "urgent", st["backlog"])
    low = _mk(client, workspace, project, "low", st["backlog"])
    fj = quote(json.dumps({"priority__in": "urgent,high"}))
    got = _ids(client.api_get(B(workspace, project) + f"/issues/?group_by=state_id&filters={fj}&cursor=30:0:0&per_page=30").json())
    assert urgent in got and low not in got


def test_no_filter_returns_all(client, workspace, project):
    st = _states(client, workspace, project)
    a = _mk(client, workspace, project, "urgent", st["backlog"])
    b = _mk(client, workspace, project, "low", st["started"])
    got = _ids(client.api_get(B(workspace, project) + "/issues/").json())
    assert {a, b} <= got
