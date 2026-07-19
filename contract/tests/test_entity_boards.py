"""Contract: the module/cycle detail boards. Two things the frontend needs that
the Go port was missing:
  1. per-module / per-cycle user-properties (GET get-or-create, PATCH merge) —
     the frontend fetches these before rendering the board; a 404 left it blank.
  2. the project issue list scoped by ?module= / ?cycle= — the module/cycle
     boards hit /projects/{id}/issues/?module=<id>, not the nested list.
"""
import pytest
from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.entity_boards


def B(ws, p):
    return f"/api/workspaces/{ws['slug']}/projects/{p['id']}"


def _count(body):
    return body["total_count"]


def _mk_issue(client, ws, p, state_id):
    return client.api_post(B(ws, p) + "/issues/", json={"name": "EB " + unique(), "state_id": state_id}).json()["id"]


def _state(client, ws, p):
    return client.api_get(B(ws, p) + "/states/").json()[0]["id"]


# ---- user-properties ------------------------------------------------------

def _assert_props(body, key):
    for k in ("id", "filters", "display_filters", "display_properties", "project", "workspace", "user", key):
        assert k in body, f"missing {k}"
    assert isinstance(body["display_filters"], dict)
    assert isinstance(body["display_properties"], dict)
    assert isinstance(body["filters"], dict)


def test_module_user_properties_get_and_patch(client, workspace, project):
    mid = client.api_post(B(workspace, project) + "/modules/", json={"name": "M " + unique()}).json()["id"]
    base = B(workspace, project) + f"/modules/{mid}/user-properties/"
    r = client.api_get(base)
    assert_status(r, 200)
    _assert_props(r.json(), "module")
    p = client.api_patch(base, json={"display_filters": {"layout": "kanban"}})
    assert p.status_code in (200, 201)
    assert p.json()["display_filters"]["layout"] == "kanban"


def test_cycle_user_properties_get_and_patch(client, workspace, project):
    cid = client.api_post(B(workspace, project) + "/cycles/", json={"name": "C " + unique()}).json()["id"]
    base = B(workspace, project) + f"/cycles/{cid}/user-properties/"
    r = client.api_get(base)
    assert_status(r, 200)
    _assert_props(r.json(), "cycle")
    p = client.api_patch(base, json={"display_filters": {"layout": "kanban"}})
    assert p.status_code in (200, 201)
    assert p.json()["display_filters"]["layout"] == "kanban"


# ---- ?module= / ?cycle= scoping on the project issue list -----------------

def test_project_issues_scoped_by_module(client, workspace, project):
    sid = _state(client, workspace, project)
    ids = [_mk_issue(client, workspace, project, sid) for _ in range(3)]
    mid = client.api_post(B(workspace, project) + "/modules/", json={"name": "M " + unique()}).json()["id"]
    client.api_post(B(workspace, project) + f"/modules/{mid}/issues/", json={"issues": ids[:2]})
    r = client.api_get(B(workspace, project) + f"/issues/?module={mid}&cursor=100:0:0&per_page=100")
    assert_status(r, 200)
    assert _count(r.json()) == 2


def test_project_issues_scoped_by_cycle(client, workspace, project):
    sid = _state(client, workspace, project)
    ids = [_mk_issue(client, workspace, project, sid) for _ in range(3)]
    cid = client.api_post(B(workspace, project) + "/cycles/", json={"name": "C " + unique()}).json()["id"]
    client.api_post(B(workspace, project) + f"/cycles/{cid}/cycle-issues/", json={"issues": ids[:1]})
    r = client.api_get(B(workspace, project) + f"/issues/?cycle={cid}&cursor=100:0:0&per_page=100")
    assert_status(r, 200)
    assert _count(r.json()) == 1
