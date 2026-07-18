"""Contract: workspace notifications (list + unread count) and user favorites."""

import pytest

from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.workspace_extras


def test_notifications_list(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/users/notifications/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_notifications_unread_count(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/users/notifications/unread/")
    assert_status(r, 200)
    assert_has_fields(
        r.json(),
        {"total_unread_notifications_count": int, "mention_unread_notifications_count": int},
        where="unread",
    )


def test_favorite_crud(client, workspace, project):
    base = f"/api/workspaces/{workspace['slug']}/user-favorites/"
    r = client.api_post(base, json={"entity_type": "project", "entity_identifier": project["id"], "name": project["name"]})
    assert_status(r, 200)  # quirk: 200, not 201
    fav = r.json()
    assert_has_fields(fav, {"id": str, "entity_type": str, "entity_identifier": str, "name": str}, where="favorite")
    assert is_uuid(fav["id"])
    assert fav["entity_type"] == "project"
    rl = client.api_get(base)
    assert_status(rl, 200)
    assert any(f["id"] == fav["id"] for f in rl.json())
    assert_status(client.api_delete(base + f"{fav['id']}/"), 204)
