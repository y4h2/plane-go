"""Contract: project-level extras not covered by test_project.py.

  - PATCH /api/workspaces/<slug>/projects/<id>/   -- feature-flag toggles.
    There is NO separate "features" endpoint (verified against Django urls/project.py
    and views/project/base.py): the module_view / cycle_view / issue_views_view /
    page_view / is_time_tracking_enabled / is_issue_type_enabled /
    guest_view_all_features flags are just regular fields on the same
    ProjectViewSet.partial_update used for name/description, returning the same
    43-key ProjectListSerializer shape (200).

    QUIRK (confirmed live): the intake toggle must be sent as `inbox_view` in the
    request body, NOT `intake_view`. The view does
    `intake_view = request.data.get("inbox_view", project.intake_view)` and then
    overwrites `request.data["intake_view"]` with that before validating, so a bare
    `{"intake_view": true}` body is silently ignored (the field is untouched).

    Only a workspace admin or project admin (role 20) may PATCH; any other
    project member gets 403.

  - GET /api/workspaces/<slug>/projects/<id>/search-issues/   (IssueSearchEndpoint)
    Bare list (NOT the cursor-pagination envelope) of a fixed 10-key `.values()`
    row, capped at 100. `search=<q>` does an OR icontains match across
    `name`, `project__identifier`, and (numeric substrings only) `sequence_id`.
    With no `search` param, all issues in scope are returned (still capped at 100).
    `workspace_search=true` widens scope to every project in the workspace instead
    of just the URL's project. A caller with no project membership gets 200 + []
    (NOT 403/404 -- the queryset filter on project membership silently yields
    nothing).

Every shape/quirk here was probed against the live Python reference server.
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_status, is_uuid

pytestmark = pytest.mark.project_extras

ROLE_MEMBER = 15
ROLE_ADMIN = 20

# The 10-key bare row returned by search-issues (`.values()` with dunder-joined
# related fields). Confirmed exact against the live server.
SEARCH_ROW_SHAPE = {
    "name": str,
    "id": str,
    "start_date": OPTIONAL(str),
    "sequence_id": int,
    "project__name": str,
    "project__identifier": str,
    "project_id": str,
    "workspace__slug": str,
    "state__name": str,
    "state__group": str,
    "state__color": str,
}

# Subset of the 43-key ProjectListSerializer (see test_project.py::PROJECT_SHAPE)
# relevant to the feature-flag toggle.
FEATURE_FLAG_FIELDS = {
    "module_view": bool,
    "cycle_view": bool,
    "issue_views_view": bool,
    "page_view": bool,
    "intake_view": bool,
    "inbox_view": bool,
    "is_time_tracking_enabled": bool,
    "is_issue_type_enabled": bool,
    "guest_view_all_features": bool,
}


def _slug_of(client, project) -> str:
    ws = client.api_get("/api/users/me/workspaces/").json()
    for w in ws:
        if w["id"] == project["workspace"]:
            return w["slug"]
    raise AssertionError(f"no workspace slug for {project['workspace']}")


def _add_project_member(owner, slug: str, project_id: str, role: int = ROLE_MEMBER):
    """Create a new user, invite to the workspace, accept, then add as a project
    member with `role`. Returns the signed-in client."""
    from lib.client import PlaneClient

    newcomer = PlaneClient(owner.base_url).sign_up()
    newcomer.whoami()
    r = owner.api_post(
        f"/api/workspaces/{slug}/invitations/",
        json={"emails": [{"email": newcomer.email, "role": role}]},
    )
    assert_status(r, 200, 201)
    invites = newcomer.api_get("/api/users/me/workspaces/invitations/").json()
    invite_id = next(i["id"] for i in invites if i["workspace"]["slug"] == slug)
    assert_status(
        newcomer.api_post("/api/users/me/workspaces/invitations/", json={"invitations": [invite_id]}),
        204,
    )
    r = owner.api_post(
        f"/api/workspaces/{slug}/projects/{project_id}/members/",
        json={"members": [{"member_id": newcomer.user_id, "role": role}]},
    )
    assert_status(r, 201)
    return newcomer


# --------------------------------------------------------------------------- #
# feature-flag toggle (PATCH .../projects/<id>/)
# --------------------------------------------------------------------------- #
def test_patch_toggles_all_feature_flags(client, project):
    slug = _slug_of(client, project)
    r = client.api_patch(
        f"/api/workspaces/{slug}/projects/{project['id']}/",
        json={
            "module_view": True,
            "cycle_view": True,
            "issue_views_view": True,
            "page_view": False,
            "is_time_tracking_enabled": True,
            "is_issue_type_enabled": True,
            "guest_view_all_features": True,
        },
    )
    assert_status(r, 200)
    body = r.json()
    assert_fields({k: body[k] for k in FEATURE_FLAG_FIELDS}, FEATURE_FLAG_FIELDS, where="feature-flags")
    assert body["module_view"] is True
    assert body["cycle_view"] is True
    assert body["issue_views_view"] is True
    assert body["page_view"] is False
    assert body["is_time_tracking_enabled"] is True
    assert body["is_issue_type_enabled"] is True
    assert body["guest_view_all_features"] is True


def test_patch_intake_toggle_uses_inbox_view_alias_not_intake_view(client, project):
    """QUIRK: sending `intake_view` directly is silently ignored; only the
    `inbox_view` alias actually flips the stored flag."""
    slug = _slug_of(client, project)
    url = f"/api/workspaces/{slug}/projects/{project['id']}/"

    r = client.api_patch(url, json={"intake_view": True})
    assert_status(r, 200)
    assert r.json()["intake_view"] is False
    assert r.json()["inbox_view"] is False

    r = client.api_patch(url, json={"inbox_view": True})
    assert_status(r, 200)
    assert r.json()["intake_view"] is True
    assert r.json()["inbox_view"] is True


def test_patch_feature_flags_forbidden_for_non_admin_member(client, workspace):
    slug = workspace["slug"]
    proj = client.create_project(slug)
    member = _add_project_member(client, slug, proj["id"], role=ROLE_MEMBER)
    r = member.api_patch(
        f"/api/workspaces/{slug}/projects/{proj['id']}/",
        json={"module_view": True},
    )
    assert_status(r, 403)
    assert r.json()["error"] == "You don't have the required permissions."


def test_patch_feature_flags_allowed_for_project_admin_non_ws_admin(client, workspace):
    """A project admin (role 20) who is NOT a workspace admin can still toggle."""
    slug = workspace["slug"]
    proj = client.create_project(slug)
    admin_member = _add_project_member(client, slug, proj["id"], role=ROLE_ADMIN)
    r = admin_member.api_patch(
        f"/api/workspaces/{slug}/projects/{proj['id']}/",
        json={"cycle_view": True},
    )
    assert_status(r, 200)
    assert r.json()["cycle_view"] is True


# --------------------------------------------------------------------------- #
# project-scoped issue search (GET .../projects/<id>/search-issues/)
# --------------------------------------------------------------------------- #
def _create_issue(client, base: str, name: str) -> dict:
    r = client.api_post(base + "/issues/", json={"name": name})
    assert_status(r, 201)
    return r.json()


def test_search_issues_matches_query_by_name_substring(client, workspace):
    slug = workspace["slug"]
    proj = client.create_project(slug)
    base = f"/api/workspaces/{slug}/projects/{proj['id']}"
    token = unique("kw")
    hit = _create_issue(client, base, f"Fix the {token} bug")
    _create_issue(client, base, "Totally unrelated issue")

    r = client.api_get(base + "/search-issues/", params={"search": token})
    assert_status(r, 200)
    rows = r.json()
    assert isinstance(rows, list) and len(rows) == 1
    assert_fields(rows[0], SEARCH_ROW_SHAPE, where="search-row")
    assert rows[0]["id"] == hit["id"]
    assert rows[0]["project_id"] == proj["id"]
    assert is_uuid(rows[0]["id"])


def test_search_issues_no_query_returns_all_in_project(client, workspace):
    slug = workspace["slug"]
    proj = client.create_project(slug)
    base = f"/api/workspaces/{slug}/projects/{proj['id']}"
    names = [f"Issue {unique()}" for _ in range(3)]
    for n in names:
        _create_issue(client, base, n)

    r = client.api_get(base + "/search-issues/")
    assert_status(r, 200)
    rows = r.json()
    assert isinstance(rows, list)
    assert len(rows) == 3
    for row in rows:
        assert_fields(row, SEARCH_ROW_SHAPE, where="search-row")


def test_search_issues_no_match_returns_empty_list(client, project):
    slug = _slug_of(client, project)
    r = client.api_get(
        f"/api/workspaces/{slug}/projects/{project['id']}/search-issues/",
        params={"search": unique("nomatch-")},
    )
    assert_status(r, 200)
    assert r.json() == []


def test_search_issues_project_scoped_by_default_excludes_other_projects(client, workspace):
    slug = workspace["slug"]
    p1 = client.create_project(slug)
    p2 = client.create_project(slug)
    token = unique("shared")
    _create_issue(client, f"/api/workspaces/{slug}/projects/{p1['id']}", f"{token} one")
    _create_issue(client, f"/api/workspaces/{slug}/projects/{p2['id']}", f"{token} two")

    r = client.api_get(
        f"/api/workspaces/{slug}/projects/{p1['id']}/search-issues/", params={"search": token}
    )
    assert_status(r, 200)
    rows = r.json()
    assert len(rows) == 1
    assert rows[0]["project_id"] == p1["id"]


def test_search_issues_workspace_search_true_spans_all_projects(client, workspace):
    slug = workspace["slug"]
    p1 = client.create_project(slug)
    p2 = client.create_project(slug)
    token = unique("shared")
    _create_issue(client, f"/api/workspaces/{slug}/projects/{p1['id']}", f"{token} one")
    _create_issue(client, f"/api/workspaces/{slug}/projects/{p2['id']}", f"{token} two")

    r = client.api_get(
        f"/api/workspaces/{slug}/projects/{p1['id']}/search-issues/",
        params={"search": token, "workspace_search": "true"},
    )
    assert_status(r, 200)
    rows = r.json()
    project_ids = {row["project_id"] for row in rows}
    assert project_ids == {p1["id"], p2["id"]}


def test_search_issues_non_project_member_returns_empty_not_error(client, project, fresh_client):
    """A user unrelated to the project's workspace still gets 200 + [] (the
    queryset's membership filter silently excludes everything), not 403/404."""
    slug = _slug_of(client, project)
    r = fresh_client.api_get(f"/api/workspaces/{slug}/projects/{project['id']}/search-issues/")
    assert_status(r, 200)
    assert r.json() == []
