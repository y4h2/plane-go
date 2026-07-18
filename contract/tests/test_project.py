"""Contract: project + project-member endpoints.

Covers the project CRUD surface the frontend depends on:
  - POST   /api/workspaces/<slug>/projects/            create (201, ProjectListSerializer)
  - GET    /api/workspaces/<slug>/projects/            list (bare list of .values() dicts)
  - GET    /api/workspaces/<slug>/projects/details/    fuller list (bare list, serializer)
  - GET    /api/workspaces/<slug>/projects/<pk>/       retrieve (200 member / 404 random / 409 ws-member-not-project-member / 403 non-ws)
  - PATCH  /api/workspaces/<slug>/projects/<pk>/        partial update (200)
  - DELETE /api/workspaces/<slug>/projects/<pk>/        destroy (204)
  - GET    /api/workspaces/<slug>/project-identifiers/  identifier availability

And project members:
  - GET  /api/workspaces/<slug>/projects/<id>/members/                bare list ({id,role,member,project,original_role,created_at})
  - POST /api/workspaces/<slug>/projects/<id>/members/                add members (201, bare list)
  - GET  /api/workspaces/<slug>/projects/<id>/members/<mid>/          retrieve one (ProjectMember shape)
  - GET  /api/workspaces/<slug>/projects/<id>/project-members/me/     caller's own membership

Every shape here was probed against the live Python reference server. Volatile
values (ids/timestamps) are type-checked, never value-matched.
"""

import uuid

import pytest

from lib.client import PlaneClient, unique
from lib.shape import OPTIONAL, ANY, assert_fields, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.project

# Role ints used across the project-member API.
ROLE_GUEST = 5
ROLE_MEMBER = 15
ROLE_ADMIN = 20

# ProjectListSerializer — the exact key set returned by create/retrieve/patch and
# each item of .../projects/details/. Confirmed exact against the live server.
PROJECT_SHAPE = {
    "id": str,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "deleted_at": OPTIONAL(str),
    "name": str,
    "description": str,
    "description_text": OPTIONAL(str, dict),
    "description_html": OPTIONAL(str, dict),
    "network": int,
    "workspace": str,
    "identifier": str,
    "default_assignee": OPTIONAL(str),
    "project_lead": OPTIONAL(str),
    "emoji": OPTIONAL(str),
    "icon_prop": OPTIONAL(str, dict),
    "module_view": bool,
    "cycle_view": bool,
    "issue_views_view": bool,
    "page_view": bool,
    "intake_view": bool,
    "is_time_tracking_enabled": bool,
    "is_issue_type_enabled": bool,
    "guest_view_all_features": bool,
    "cover_image": OPTIONAL(str),
    "cover_image_asset": OPTIONAL(str),
    "estimate": OPTIONAL(str),
    "archive_in": int,
    "close_in": int,
    "logo_props": dict,
    "default_state": OPTIONAL(str),
    "archived_at": OPTIONAL(str),
    "timezone": str,
    "external_source": OPTIONAL(str),
    "external_id": OPTIONAL(str),
    # explicit / annotated extras on ProjectListSerializer
    "is_favorite": bool,
    "sort_order": float,
    "member_role": OPTIONAL(int),
    "anchor": OPTIONAL(str),
    "members": list,
    "cover_image_url": OPTIONAL(str),
    "inbox_view": bool,
    "next_work_item_sequence": int,
}

# GET .../projects/ returns a subset of .values() dicts plus annotated
# member_role / intake_count. Noisy annotation-driven set -> pin a stable subset.
PROJECT_LIST_ITEM_SUBSET = {
    "id": str,
    "name": str,
    "identifier": str,
    "workspace": str,
    "network": int,
    "member_role": OPTIONAL(int),
    "intake_count": int,
    "sort_order": float,
    "logo_props": dict,
    "created_at": str,
}

# One row from GET/POST .../members/ (bare list). Confirmed exact key set.
MEMBER_ROW_SHAPE = {
    "id": str,
    "role": int,
    "member": str,
    "project": str,
    "original_role": int,
    "created_at": str,
}


# --------------------------------------------------------------------------- #
# helpers
# --------------------------------------------------------------------------- #
def _create_project(client, slug, **overrides) -> "tuple[dict, object]":
    body = {"name": f"P {unique()}", "identifier": unique("P")[:8].upper()}
    body.update(overrides)
    r = client.api_post(f"/api/workspaces/{slug}/projects/", json=body)
    return body, r


def _add_workspace_member(owner: PlaneClient, slug: str, role: int = ROLE_MEMBER) -> PlaneClient:
    """Create a brand-new user, invite them to `slug`, accept the invite, return
    the signed-in client. The user is now a workspace member but NOT a member of
    any project in it."""
    newcomer = PlaneClient(owner.base_url).sign_up()
    newcomer.whoami()
    r = owner.api_post(
        f"/api/workspaces/{slug}/invitations/",
        json={"emails": [{"email": newcomer.email, "role": role}]},
    )
    assert_status(r, 200, 201)
    invites = newcomer.api_get("/api/users/me/workspaces/invitations/").json()
    invite_id = next(i["id"] for i in invites if i["workspace"]["slug"] == slug)
    r = newcomer.api_post(
        "/api/users/me/workspaces/invitations/", json={"invitations": [invite_id]}
    )
    assert_status(r, 204)
    return newcomer


# --------------------------------------------------------------------------- #
# create
# --------------------------------------------------------------------------- #
def test_create_project_returns_201_and_full_shape(client, workspace):
    slug = workspace["slug"]
    body, r = _create_project(client, slug)
    assert_status(r, 201)
    proj = r.json()
    assert_fields(proj, PROJECT_SHAPE, where="project")
    assert is_uuid(proj["id"])
    assert is_uuid(proj["workspace"])
    assert proj["name"] == body["name"]
    assert proj["identifier"] == body["identifier"]
    # creator is admin and is the sole member on a fresh project.
    assert proj["member_role"] == ROLE_ADMIN
    assert all(is_uuid(m) for m in proj["members"])


def test_create_project_requires_name(client, workspace):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/",
        json={"identifier": unique("P")[:8].upper()},
    )
    assert_status(r, 400)
    assert "name" in r.json()


def test_create_project_requires_identifier(client, workspace):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/",
        json={"name": f"P {unique()}"},
    )
    assert_status(r, 400)
    assert "identifier" in r.json()


# --------------------------------------------------------------------------- #
# list
# --------------------------------------------------------------------------- #
def test_list_projects_is_bare_list_of_values(client, project):
    r = client.api_get(f"/api/workspaces/{_slug_of(client, project)}/projects/")
    assert_status(r, 200)
    lst = r.json()
    assert isinstance(lst, list) and lst, "expected a non-empty bare list"
    for item in lst:
        assert_has_fields(item, PROJECT_LIST_ITEM_SUBSET, where="project-list-item")
    ids = [i["id"] for i in lst]
    assert project["id"] in ids


def test_details_list_is_bare_list_with_full_shape(client, project):
    r = client.api_get(f"/api/workspaces/{_slug_of(client, project)}/projects/details/")
    assert_status(r, 200)
    lst = r.json()
    assert isinstance(lst, list) and lst
    for item in lst:
        assert_fields(item, PROJECT_SHAPE, where="project-details-item")
    assert project["id"] in [i["id"] for i in lst]


# --------------------------------------------------------------------------- #
# retrieve
# --------------------------------------------------------------------------- #
def test_retrieve_project_as_member_returns_200_and_shape(client, project):
    r = client.api_get(f"/api/workspaces/{_slug_of(client, project)}/projects/{project['id']}/")
    assert_status(r, 200)
    assert_fields(r.json(), PROJECT_SHAPE, where="project-retrieve")


def test_retrieve_unknown_project_returns_404(client, project):
    slug = _slug_of(client, project)
    r = client.api_get(f"/api/workspaces/{slug}/projects/{uuid.uuid4()}/")
    assert_status(r, 404)


def test_retrieve_project_as_workspace_member_non_project_member_returns_409(client, project):
    """A user who belongs to the workspace but not the (public) project gets a 409
    with an explanatory error, not a 200 leak."""
    slug = _slug_of(client, project)
    ws_member = _add_workspace_member(client, slug)
    r = ws_member.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/")
    assert_status(r, 409)
    assert r.json()["error"] == "You are not a member of this project"


def test_retrieve_project_as_non_workspace_member_returns_403(client, project, fresh_client):
    """A user with no relationship to the workspace is blocked by permissions (403)."""
    slug = _slug_of(client, project)
    r = fresh_client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/")
    assert_status(r, 403)


# --------------------------------------------------------------------------- #
# update / delete
# --------------------------------------------------------------------------- #
def test_patch_project_updates_and_returns_shape(client, project):
    slug = _slug_of(client, project)
    new_name = f"Renamed {unique()}"
    r = client.api_patch(
        f"/api/workspaces/{slug}/projects/{project['id']}/",
        json={"name": new_name, "description": "updated by contract test"},
    )
    assert_status(r, 200)
    proj = r.json()
    assert_fields(proj, PROJECT_SHAPE, where="project-patch")
    assert proj["name"] == new_name
    assert proj["description"] == "updated by contract test"


def test_delete_project_returns_204(client, workspace):
    slug = workspace["slug"]
    proj = client.create_project(slug)
    r = client.api_delete(f"/api/workspaces/{slug}/projects/{proj['id']}/")
    assert_status(r, 204)
    # gone afterwards
    assert_status(client.api_get(f"/api/workspaces/{slug}/projects/{proj['id']}/"), 404)


# --------------------------------------------------------------------------- #
# project-identifiers
# --------------------------------------------------------------------------- #
def test_project_identifiers_requires_name(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/project-identifiers/")
    assert_status(r, 400)
    assert "error" in r.json()


def test_project_identifiers_reports_existing(client, project):
    slug = _slug_of(client, project)
    r = client.api_get(
        f"/api/workspaces/{slug}/project-identifiers/", params={"name": project["identifier"]}
    )
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"exists": int, "identifiers": list}, where="project-identifiers")
    assert body["exists"] >= 1
    for row in body["identifiers"]:
        assert_has_fields(row, {"name": str, "project": str}, where="identifier-row")


# --------------------------------------------------------------------------- #
# project members
# --------------------------------------------------------------------------- #
def test_members_list_is_bare_list_with_row_shape(client, project):
    slug = _slug_of(client, project)
    r = client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/members/")
    assert_status(r, 200)
    rows = r.json()
    assert isinstance(rows, list) and rows, "creator should be a member"
    for row in rows:
        assert_fields(row, MEMBER_ROW_SHAPE, where="member-row")
        assert is_uuid(row["member"])
        assert is_uuid(row["project"])
    # creator is present as admin
    assert any(row["role"] == ROLE_ADMIN for row in rows)


def test_add_member_returns_201_and_row_shape(client, workspace):
    slug = workspace["slug"]
    proj = client.create_project(slug)
    new_member = _add_workspace_member(client, slug)
    r = client.api_post(
        f"/api/workspaces/{slug}/projects/{proj['id']}/members/",
        json={"members": [{"member_id": new_member.user_id, "role": ROLE_MEMBER}]},
    )
    assert_status(r, 201)
    rows = r.json()
    assert isinstance(rows, list) and len(rows) == 1
    assert_fields(rows[0], MEMBER_ROW_SHAPE, where="added-member")
    assert rows[0]["role"] == ROLE_MEMBER
    assert rows[0]["member"] == new_member.user_id


def test_add_member_empty_list_rejected(client, project):
    slug = _slug_of(client, project)
    r = client.api_post(
        f"/api/workspaces/{slug}/projects/{project['id']}/members/", json={"members": []}
    )
    assert_status(r, 400)
    assert "error" in r.json()


def test_add_non_workspace_member_rejected(client, project, fresh_client):
    """A user who is not in the workspace cannot be added to a project."""
    slug = _slug_of(client, project)
    r = client.api_post(
        f"/api/workspaces/{slug}/projects/{project['id']}/members/",
        json={"members": [{"member_id": fresh_client.user_id, "role": ROLE_MEMBER}]},
    )
    assert_status(r, 400, 404)


def test_retrieve_single_member(client, project):
    slug = _slug_of(client, project)
    rows = client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/members/").json()
    mid = rows[0]["id"]
    r = client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/members/{mid}/")
    assert_status(r, 200)
    body = r.json()
    # the single-member retrieve expands member/project/workspace into nested objects.
    assert_has_fields(
        body,
        {"id": str, "role": int, "member": dict, "project": dict, "workspace": dict},
        where="member-detail",
    )
    assert_has_fields(body["member"], {"id": str, "display_name": str}, where="member-detail.member")
    assert body["project"]["id"] == project["id"]


def test_project_members_me_returns_own_membership(client, project):
    slug = _slug_of(client, project)
    r = client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/project-members/me/")
    assert_status(r, 200)
    body = r.json()
    # `me` expands member/project/workspace into nested objects.
    assert_has_fields(
        body,
        {
            "id": str,
            "role": int,
            "member": dict,
            "project": dict,
            "workspace": dict,
        },
        where="project-members-me",
    )
    assert body["role"] == ROLE_ADMIN
    assert_has_fields(body["member"], {"id": str, "display_name": str}, where="me.member")
    assert body["project"]["id"] == project["id"]
    assert body["workspace"]["slug"] == slug


# --------------------------------------------------------------------------- #
# internal: resolve the slug a project fixture lives in.
# The `project` fixture returns a ProjectListSerializer dict whose `workspace` is
# a UUID, not a slug. We recover the slug from the caller's workspace list.
# --------------------------------------------------------------------------- #
def _slug_of(client: PlaneClient, project: dict) -> str:
    ws = client.api_get("/api/users/me/workspaces/").json()
    for w in ws:
        if w["id"] == project["workspace"]:
            return w["slug"]
    raise AssertionError(f"no workspace slug for {project['workspace']}")
