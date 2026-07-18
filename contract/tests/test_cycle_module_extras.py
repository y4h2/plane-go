"""Contract: cycle/module favorites and cycle issue-transfer."""

import pytest

from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.cycle_module_extras


def _proj_base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _cycle(client, workspace, project) -> str:
    r = client.api_post(_proj_base(workspace, project) + "/cycles/", json={"name": "Cy " + unique()})
    assert_status(r, 201)
    return r.json()["id"]


def _module(client, workspace, project) -> str:
    r = client.api_post(_proj_base(workspace, project) + "/modules/", json={"name": "Mo " + unique()})
    assert_status(r, 201)
    return r.json()["id"]


def test_favorite_cycle(client, workspace, project):
    base = _proj_base(workspace, project)
    cid = _cycle(client, workspace, project)
    assert_status(client.api_post(base + "/user-favorite-cycles/", json={"cycle": cid}), 204)
    assert_status(client.api_delete(base + f"/user-favorite-cycles/{cid}/"), 204)


def test_favorite_module(client, workspace, project):
    base = _proj_base(workspace, project)
    mid = _module(client, workspace, project)
    assert_status(client.api_post(base + "/user-favorite-modules/", json={"module": mid}), 204)
    assert_status(client.api_delete(base + f"/user-favorite-modules/{mid}/"), 204)


def test_transfer_cycle_issues(client, workspace, project):
    base = _proj_base(workspace, project)
    c1 = _cycle(client, workspace, project)
    c2 = _cycle(client, workspace, project)
    r = client.api_post(base + f"/cycles/{c1}/transfer-issues/", json={"new_cycle_id": c2})
    assert_status(r, 200)
    assert r.json() == {"message": "Success"}
