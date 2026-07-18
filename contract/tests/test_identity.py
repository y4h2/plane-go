"""Contract: boot/identity endpoints the frontend hits on load.

Covers user settings/profile, instance config, workspace-member "me", the
workspace members list, and the per-workspace project-roles map. Payloads are
large and defaults-heavy, so we pin a stable subset of keys + types rather than
the whole structure.
"""

import pytest

from lib.shape import ANY, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.identity


def test_user_settings(client):
    r = client.api_get("/api/users/me/settings/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"id": str, "email": str, "workspace": dict}, where="settings")
    assert_has_fields(
        body["workspace"],
        {"fallback_workspace_id": ANY, "fallback_workspace_slug": ANY, "invites": int},
        where="settings.workspace",
    )


def test_user_profile(client):
    r = client.api_get("/api/users/me/profile/")
    assert_status(r, 200)
    assert_has_fields(
        r.json(),
        {
            "id": str,
            "theme": dict,
            "onboarding_step": dict,
            "is_onboarded": bool,
            "is_tour_completed": bool,
            "language": str,
        },
        where="profile",
    )


def test_instance_config(client):
    r = client.api_get("/api/instances/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"config": dict, "instance": dict}, where="instances")
    assert_has_fields(
        body["config"],
        {"enable_signup": bool, "is_email_password_enabled": bool, "app_base_url": str},
        where="instances.config",
    )
    assert_has_fields(body["instance"], {"is_setup_done": bool}, where="instances.instance")


def test_workspace_member_me(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/workspace-members/me/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(
        body,
        {"id": str, "role": int, "draft_issue_count": int, "view_props": dict, "company_role": str},
        where="ws-member-me",
    )
    assert body["role"] == 20  # creator is admin


def test_workspace_members_list(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/members/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list) and len(body) >= 1
    me = [m for m in body if isinstance(m.get("member"), dict) and m["member"].get("id") == client.user_id]
    assert len(me) == 1, "creator must appear in the workspace members list"
    row = me[0]
    assert_has_fields(row, {"id": str, "member": dict, "role": int}, where="ws-member")
    assert_has_fields(row["member"], {"id": str, "display_name": str}, where="ws-member.member")


def test_project_roles(client, workspace, project):
    r = client.api_get(f"/api/users/me/workspaces/{workspace['slug']}/project-roles/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict)
    assert project["id"] in body
    assert body[project["id"]] == 20
    for k, v in body.items():
        assert is_uuid(k) and isinstance(v, int)
