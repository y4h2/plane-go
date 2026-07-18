"""Contract: saved views (workspace-level and project-level)."""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.views

VIEW_SHAPE = {
    "id": str,
    "name": str,
    "description": str,
    "access": int,
    "query": dict,
    "filters": dict,
    "display_filters": dict,
    "display_properties": dict,
    "logo_props": dict,
    "sort_order": (int, float),
    "is_locked": bool,
    "workspace": str,
    "project": OPTIONAL(str),
    "created_at": str,
    "updated_at": str,
}


def test_create_and_list_workspace_view(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/views/"
    r = client.api_post(base, json={"name": "V " + unique()})
    assert_status(r, 201)
    view = r.json()
    assert_has_fields(view, VIEW_SHAPE, where="ws-view")
    assert is_uuid(view["id"])
    rl = client.api_get(base)
    assert_status(rl, 200)
    assert any(v["id"] == view["id"] for v in rl.json())


def test_create_and_list_project_view(client, workspace, project):
    base = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/views/"
    r = client.api_post(base, json={"name": "PV " + unique()})
    assert_status(r, 201)
    view = r.json()
    assert_has_fields(view, VIEW_SHAPE, where="proj-view")
    assert view["project"] == project["id"]
    rl = client.api_get(base)
    assert_status(rl, 200)
    assert any(v["id"] == view["id"] for v in rl.json())


def test_patch_view(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/views/"
    vid = client.api_post(base, json={"name": "V " + unique()}).json()["id"]
    r = client.api_patch(base + f"{vid}/", json={"name": "Renamed View"})
    assert_status(r, 200)
    assert r.json()["name"] == "Renamed View"


def test_delete_view(client, workspace):
    base = f"/api/workspaces/{workspace['slug']}/views/"
    vid = client.api_post(base, json={"name": "V " + unique()}).json()["id"]
    assert_status(client.api_delete(base + f"{vid}/"), 204)
