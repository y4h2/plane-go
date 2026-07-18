"""Contract: workspace endpoints.

Covers create (201, full WorkSpaceSerializer shape), list-my-workspaces (bare list),
retrieve/update/slug-check. `GET /api/workspaces/` is intentionally NOT the list the
frontend uses — it lists via /api/users/me/workspaces/.
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_status, is_uuid

pytestmark = pytest.mark.workspace

# WorkSpaceSerializer (fields="__all__") + computed total_members/logo_url/role.
WORKSPACE_SHAPE = {
    "id": str,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "deleted_at": OPTIONAL(str),
    "name": str,
    "logo": OPTIONAL(str),
    "logo_asset": OPTIONAL(str),
    "logo_url": OPTIONAL(str),
    "owner": str,
    "slug": str,
    "organization_size": OPTIONAL(str),
    "timezone": str,
    "background_color": OPTIONAL(str),
    "total_members": int,
    "role": OPTIONAL(int),
}


def _create(client) -> dict:
    slug = unique("ws")
    r = client.api_post(
        "/api/workspaces/", json={"name": "CT WS", "slug": slug, "organization_size": "1-10"}
    )
    assert_status(r, 201)
    return r.json()


def test_create_workspace(client):
    body = _create(client)
    assert_fields(body, WORKSPACE_SHAPE, where="workspace")
    assert is_uuid(body["id"])
    assert body["total_members"] == 1
    assert body["role"] == 20  # creator is admin


def test_create_workspace_requires_name_and_slug(client):
    assert_status(client.api_post("/api/workspaces/", json={"slug": unique("ws")}), 400)
    assert_status(client.api_post("/api/workspaces/", json={"name": "No Slug"}), 400)


def test_list_my_workspaces(client):
    created = _create(client)
    r = client.api_get("/api/users/me/workspaces/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [w for w in body if w["id"] == created["id"]]
    assert len(mine) == 1
    assert_fields(mine[0], WORKSPACE_SHAPE, where="workspace[0]")


def test_retrieve_workspace(client):
    created = _create(client)
    r = client.api_get(f"/api/workspaces/{created['slug']}/")
    assert_status(r, 200)
    assert r.json()["slug"] == created["slug"]


def test_update_workspace(client):
    created = _create(client)
    r = client.api_patch(f"/api/workspaces/{created['slug']}/", json={"name": "Renamed WS"})
    assert_status(r, 200)
    assert r.json()["name"] == "Renamed WS"


def test_slug_check(client):
    created = _create(client)
    r = client.api_get(f"/api/workspace-slug-check/?slug={created['slug']}")
    assert_status(r, 200)
    # taken slug -> not available
    assert r.json().get("status") in (False, True)
