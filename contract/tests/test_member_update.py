"""Contract: updating a project member's role (completes the members CRUD)."""

import pytest

from lib.client import PlaneClient, unique
from lib.shape import assert_has_fields, assert_status

pytestmark = pytest.mark.member_update

ROLE_MEMBER = 15
ROLE_GUEST = 5


def _add_workspace_member(owner: PlaneClient, slug: str) -> PlaneClient:
    """Create a user, invite them to `slug`, accept — returns the signed-in client."""
    newcomer = PlaneClient(owner.base_url).sign_up()
    newcomer.whoami()
    r = owner.api_post(f"/api/workspaces/{slug}/invitations/", json={"emails": [{"email": newcomer.email, "role": ROLE_MEMBER}]})
    assert_status(r, 200, 201)
    invites = newcomer.api_get("/api/users/me/workspaces/invitations/").json()
    invite_id = next(i["id"] for i in invites if i["workspace"]["slug"] == slug)
    assert_status(newcomer.api_post("/api/users/me/workspaces/invitations/", json={"invitations": [invite_id]}), 204)
    return newcomer


def test_update_project_member_role(client, workspace, project):
    slug = workspace["slug"]
    base = f"/api/workspaces/{slug}/projects/{project['id']}"
    newcomer = _add_workspace_member(client, slug)

    # add the newcomer to the project as MEMBER
    r = client.api_post(base + "/members/", json={"members": [{"member_id": newcomer.user_id, "role": ROLE_MEMBER}]})
    assert_status(r, 201)
    member_row_id = r.json()[0]["id"]

    # promote/demote their role
    up = client.api_patch(base + f"/members/{member_row_id}/", json={"role": ROLE_GUEST})
    assert_status(up, 200)
    assert_has_fields(up.json(), {"id": str, "role": int}, where="member-update")
    assert up.json()["role"] == ROLE_GUEST
