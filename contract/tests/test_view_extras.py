"""Contract: user-favorite-views (project-scoped view favoriting) and
global-view-issues (workspace-level, cross-project issue list used by saved
views).

Confirmed quirks against the live Python reference server
(apps/api/plane/app/views/view/base.py):

- `IssueViewFavoriteViewSet` wires `GET .../user-favorite-views/` to `list`,
  but the view class defines no `list()` override and no `serializer_class`.
  DRF's default `ModelViewSet.list()` therefore blows up with an
  AssertionError, which the base view's generic exception handler downgrades
  to a bare 500 `{"error": "Something went wrong please try again later"}` --
  for ANY authenticated caller, regardless of workspace/project membership or
  whether the project even exists. We replicate the 500 verbatim.
- POST create does NOT validate that `view` refers to a real IssueView row
  (entity_identifier is a bare UUID field, not an FK) -- favoriting a
  made-up view id still returns 204. A missing `view` key is also accepted
  (204, null entity_identifier). A non-UUID `view` value is a 400
  `{"error": "Please provide valid detail"}` (Django's UUID field raising a
  ValidationError).
- POSTing the same (view, user) pair twice is a 400
  `{"error": "The payload is not valid"}` (unique constraint -> IntegrityError).
- DELETE keys on (project, user, workspace, view_id) -- deleting a favorite
  that doesn't exist (already deleted, or never created) is a 404
  `{"error": "The required object does not exist."}`.
- Both endpoints require the caller to be an ADMIN/MEMBER *project* member;
  a non-member (or workspace non-member) gets 403
  `{"error": "You don't have the required permissions."}`.

- `WorkspaceViewIssuesViewSet.list` reuses the issue-family cursor-pagination
  envelope and a `.values()`-like row shape (ViewIssueListSerializer) that is
  the same key set as project-scoped issue list PLUS an extra `state__group`
  field.
- It aggregates issues across every project in the workspace the caller is
  an active project member of.
- The legacy `issue_filters()` query params work (e.g. `?priority=`,
  `?project=`, both comma-separated) but the *filterset* `project_id=`
  param is a confirmed no-op on this endpoint (verified empirically: it
  neither narrows the result set nor errors) -- NOT asserted here since it's
  a bug we don't replicate.
- `?group_by=` is a confirmed no-op here: the view never passes
  `group_by_field_name` into `paginate()`, so results always stay a flat
  list with `grouped_by: null` -- even for `group_by=state`, which the
  *project-scoped* issue list rejects with 400. This is a genuine behavioral
  difference from `/issues/` at the project scope.
- Requires WORKSPACE-level membership (any of ADMIN/MEMBER/GUEST); a
  non-member (or bad workspace slug) gets 403.
"""

import pytest

from lib.client import unique
from lib.shape import ANY, OPTIONAL, assert_envelope, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.view_extras


def B(workspace, project=None) -> str:
    base = f"/api/workspaces/{workspace['slug']}"
    if project is not None:
        base += f"/projects/{project['id']}"
    return base


GLOBAL_ISSUE_SHAPE = {
    "id": str,
    "name": str,
    "state_id": OPTIONAL(str),
    "sort_order": (int, float),
    "completed_at": OPTIONAL(str),
    "estimate_point": ANY,
    "priority": str,
    "start_date": OPTIONAL(str),
    "target_date": OPTIONAL(str),
    "sequence_id": int,
    "project_id": str,
    "parent_id": OPTIONAL(str),
    "cycle_id": OPTIONAL(str),
    "module_ids": list,
    "label_ids": list,
    "assignee_ids": list,
    "sub_issues_count": OPTIONAL(int),
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "attachment_count": OPTIONAL(int),
    "link_count": OPTIONAL(int),
    "is_draft": bool,
    "archived_at": OPTIONAL(str),
    "state__group": OPTIONAL(str),
}


def _create_view(client, workspace, project) -> dict:
    r = client.api_post(B(workspace, project) + "/views/", json={"name": "V " + unique()})
    assert_status(r, 201)
    return r.json()


def _create_issue(client, workspace, project, priority=None) -> dict:
    body = {"name": "Issue " + unique()}
    if priority:
        body["priority"] = priority
    r = client.api_post(B(workspace, project) + "/issues/", json=body)
    assert_status(r, 201)
    return r.json()


# ---- user-favorite-views ---------------------------------------------------


def test_favorite_view_list_is_a_500(client, workspace, project):
    # Confirmed quirk: GET on this endpoint always 500s, regardless of state.
    r = client.api_get(B(workspace, project) + "/user-favorite-views/")
    assert_status(r, 500)
    assert r.json() == {"error": "Something went wrong please try again later"}


def test_favorite_view_create_and_delete(client, workspace, project):
    view = _create_view(client, workspace, project)
    base = B(workspace, project) + "/user-favorite-views/"

    r = client.api_post(base, json={"view": view["id"]})
    assert_status(r, 204)

    # duplicate favorite -> 400 (unique constraint)
    r = client.api_post(base, json={"view": view["id"]})
    assert_status(r, 400)
    assert r.json() == {"error": "The payload is not valid"}

    r = client.api_delete(base + f"{view['id']}/")
    assert_status(r, 204)

    # deleting again -> 404
    r = client.api_delete(base + f"{view['id']}/")
    assert_status(r, 404)
    assert r.json() == {"error": "The required object does not exist."}


def test_favorite_view_create_does_not_validate_view_exists(client, workspace, project):
    import uuid as uuidlib

    fake_view = str(uuidlib.uuid4())
    base = B(workspace, project) + "/user-favorite-views/"
    r = client.api_post(base, json={"view": fake_view})
    assert_status(r, 204)
    # and it can be deleted the same way
    r = client.api_delete(base + f"{fake_view}/")
    assert_status(r, 204)


def test_favorite_view_create_missing_view_key(client, workspace, project):
    base = B(workspace, project) + "/user-favorite-views/"
    r = client.api_post(base, json={})
    assert_status(r, 204)


def test_favorite_view_create_invalid_uuid(client, workspace, project):
    base = B(workspace, project) + "/user-favorite-views/"
    r = client.api_post(base, json={"view": "not-a-uuid"})
    assert_status(r, 400)
    assert r.json() == {"error": "Please provide valid detail"}


def test_favorite_view_delete_nonexistent_returns_404(client, workspace, project):
    import uuid as uuidlib

    base = B(workspace, project) + "/user-favorite-views/"
    r = client.api_delete(base + f"{uuidlib.uuid4()}/")
    assert_status(r, 404)


def test_favorite_view_non_member_forbidden(client, workspace, project, fresh_client):
    view = _create_view(client, workspace, project)
    base = B(workspace, project) + "/user-favorite-views/"
    # GET always 500s (see test_favorite_view_list_is_a_500) regardless of
    # membership -- the AssertionError fires before any permission check.
    assert_status(fresh_client.api_get(base), 500)
    assert_status(fresh_client.api_post(base, json={"view": view["id"]}), 403)
    assert_status(fresh_client.api_delete(base + f"{view['id']}/"), 403)


# ---- global-view-issues -----------------------------------------------------


def test_global_view_issues_envelope_shape(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_get(B(workspace) + "/issues/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="global-issues", grouped=False)
    assert isinstance(body["results"], list)
    ids = {it["id"] for it in body["results"]}
    assert issue["id"] in ids
    match = next(it for it in body["results"] if it["id"] == issue["id"])
    assert_has_fields(match, GLOBAL_ISSUE_SHAPE, where="global-issues.result")
    assert is_uuid(match["id"])


def test_global_view_issues_spans_projects(client, workspace, project):
    # A second project in the same workspace.
    project2 = client.create_project(workspace["slug"])
    i1 = _create_issue(client, workspace, project, priority="high")
    i2 = _create_issue(client, workspace, project2, priority="high")

    r = client.api_get(B(workspace) + "/issues/")
    assert_status(r, 200)
    body = r.json()
    ids_by_project = {it["id"]: it["project_id"] for it in body["results"]}
    assert ids_by_project.get(i1["id"]) == project["id"]
    assert ids_by_project.get(i2["id"]) == project2["id"]


def test_global_view_issues_priority_filter(client, workspace, project):
    hi = _create_issue(client, workspace, project, priority="high")
    lo = _create_issue(client, workspace, project, priority="low")

    r = client.api_get(B(workspace) + "/issues/?priority=high")
    assert_status(r, 200)
    body = r.json()
    ids = {it["id"] for it in body["results"]}
    assert hi["id"] in ids
    assert lo["id"] not in ids
    assert all(it["priority"] == "high" for it in body["results"])


def test_global_view_issues_project_filter(client, workspace, project):
    project2 = client.create_project(workspace["slug"])
    i1 = _create_issue(client, workspace, project)
    i2 = _create_issue(client, workspace, project2)

    r = client.api_get(B(workspace) + f"/issues/?project={project['id']}")
    assert_status(r, 200)
    body = r.json()
    ids = {it["id"] for it in body["results"]}
    assert i1["id"] in ids
    assert i2["id"] not in ids
    assert all(it["project_id"] == project["id"] for it in body["results"])


def test_global_view_issues_group_by_is_ignored(client, workspace, project):
    # Confirmed quirk: unlike the project-scoped issue list, group_by is
    # never wired up here -- results stay a flat list and grouped_by is null,
    # for ANY value including ones the project-scoped list would reject.
    issue = _create_issue(client, workspace, project)

    for group_by in ("priority", "state"):
        r = client.api_get(B(workspace) + f"/issues/?group_by={group_by}")
        assert_status(r, 200)
        body = r.json()
        assert body["grouped_by"] is None
        assert isinstance(body["results"], list)
        assert any(it["id"] == issue["id"] for it in body["results"])


def test_global_view_issues_non_member_forbidden(client, workspace, fresh_client):
    r = fresh_client.api_get(B(workspace) + "/issues/")
    assert_status(r, 403)


def test_global_view_issues_bad_workspace_slug_forbidden(client):
    r = client.api_get("/api/workspaces/does-not-exist-ws-slug/issues/")
    assert_status(r, 403)
