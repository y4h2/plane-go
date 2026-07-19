"""Contract: cycle-issues and module-issues lists honor ?group_by= and return
each group as a {results, total_results} sub-envelope — the shape the frontend's
Kanban/grouped renderer reads. A bare list here makes the cycle/module board
render empty (regression guard for the grouped-envelope fix).
"""
import pytest
from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.grouped_boards


def _wp(ws, p):
    return f"/api/workspaces/{ws['slug']}/projects/{p['id']}"


def _seed_issue(client, ws, p):
    states = client.api_get(_wp(ws, p) + "/states/").json()
    sid = states[0]["id"]
    i = client.api_post(_wp(ws, p) + "/issues/", json={"name": "GB " + unique(), "state_id": sid}).json()
    return i["id"]


def _assert_grouped(body, field):
    assert body["grouped_by"] == field
    res = body["results"]
    assert isinstance(res, dict) and res, "results must be a non-empty grouped dict"
    for key, group in res.items():
        assert isinstance(group, dict), f"group {key!r} must be a sub-envelope, got {type(group).__name__}"
        assert isinstance(group.get("results"), list), f"group {key!r} missing results list"
        assert isinstance(group.get("total_results"), int), f"group {key!r} missing total_results"


def test_cycle_issues_grouped_by_state(client, workspace, project):
    iid = _seed_issue(client, workspace, project)
    cid = client.api_post(_wp(workspace, project) + "/cycles/", json={"name": "Sprint " + unique()}).json()["id"]
    client.api_post(_wp(workspace, project) + f"/cycles/{cid}/cycle-issues/", json={"issues": [iid]})
    r = client.api_get(_wp(workspace, project) + f"/cycles/{cid}/cycle-issues/?group_by=state_id&cursor=30:0:0&per_page=30")
    assert_status(r, 200)
    _assert_grouped(r.json(), "state_id")


def test_cycle_issues_invalid_group_by_400(client, workspace, project):
    cid = client.api_post(_wp(workspace, project) + "/cycles/", json={"name": "Sprint " + unique()}).json()["id"]
    r = client.api_get(_wp(workspace, project) + f"/cycles/{cid}/cycle-issues/?group_by=state&cursor=30:0:0&per_page=30")
    assert_status(r, 400)


def test_module_issues_grouped_by_state(client, workspace, project):
    iid = _seed_issue(client, workspace, project)
    mid = client.api_post(_wp(workspace, project) + "/modules/", json={"name": "M " + unique()}).json()["id"]
    client.api_post(_wp(workspace, project) + f"/modules/{mid}/issues/", json={"issues": [iid]})
    r = client.api_get(_wp(workspace, project) + f"/modules/{mid}/issues/?group_by=state_id&cursor=30:0:0&per_page=30")
    assert_status(r, 200)
    _assert_grouped(r.json(), "state_id")


def test_module_issues_invalid_group_by_400(client, workspace, project):
    mid = client.api_post(_wp(workspace, project) + "/modules/", json={"name": "M " + unique()}).json()["id"]
    r = client.api_get(_wp(workspace, project) + f"/modules/{mid}/issues/?group_by=state&cursor=30:0:0&per_page=30")
    assert_status(r, 400)
