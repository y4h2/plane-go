"""Contract: issue extras — draft issues + bulk-update-dates.

Confirmed quirks against the live Python reference server:

Draft issues (WORKSPACE-scoped: /api/workspaces/<slug>/draft-issues/):
- CREATE returns a **single bare `.values()` dict** (NOT an envelope), 201.
  The key set is the DraftIssueSerializer 21-field shape -- notably it has
  `type_id` and `cycle_id`, and does NOT carry `sequence_id`, `is_draft`, or
  `deleted_at` (unlike the core issue `.values()`).
- CREATE does **not** validate `name` (a nameless draft is a 201 with
  `name=null`); `project_id` may be null.
- CREATE with start_date > target_date is a **400** with a
  `{"non_field_errors": [...]}` body.
- LIST returns the cursor-pagination **envelope**; each row is the same shape.
- RETRIEVE (of your own draft) returns the same shape; an unknown pk is 404.
- PATCH returns **204 with an EMPTY body**; DELETE returns 204.
- A non-member of the workspace (or an unknown slug) gets **403**.

Draft -> issue (/api/workspaces/<slug>/draft-to-issue/<draft_id>/):
- POST promotes a draft to a real issue: 201 with the (larger)
  IssueCreateSerializer shape (`is_draft=false`, a real `sequence_id`, plus
  `project`/`workspace`). A draft with no project is a **400**.

Bulk-update-dates (/api/workspaces/<slug>/projects/<id>/issue-dates/):
- POST body `{"updates": [{"id","start_date","target_date"}]}`; returns 200
  `{"message": "Issues updated successfully"}` and persists the dates.
- If a resulting start_date would exceed target_date -> **400**
  `{"message": "Start date cannot exceed target date"}`.
- An empty `updates` list is a no-op 200; a non-member gets 403.
"""

import pytest

from lib.client import unique
from lib.shape import (
    ANY,
    OPTIONAL,
    assert_envelope,
    assert_fields,
    assert_has_fields,
    assert_status,
    is_uuid,
)

pytestmark = pytest.mark.issue_extras


def W(workspace) -> str:
    return f"/api/workspaces/{workspace['slug']}"


def P(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


# The exact DraftIssueSerializer / create `.values()` key set (21 fields).
DRAFT_SHAPE = {
    "id": str,
    "name": OPTIONAL(str),
    "state_id": OPTIONAL(str),
    "sort_order": (int, float),
    "completed_at": OPTIONAL(str),
    "estimate_point": ANY,
    "priority": str,
    "start_date": OPTIONAL(str),
    "target_date": OPTIONAL(str),
    "project_id": OPTIONAL(str),
    "parent_id": OPTIONAL(str),
    "cycle_id": OPTIONAL(str),
    "module_ids": list,
    "label_ids": list,
    "assignee_ids": list,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "type_id": OPTIONAL(str),
    "description_html": str,
}


def _make_draft(client, workspace, project=None, **extra):
    body = {"name": "Draft " + unique()}
    if project is not None:
        body["project_id"] = project["id"]
    body.update(extra)
    r = client.api_post(W(workspace) + "/draft-issues/", json=body)
    assert_status(r, 201)
    return r.json()


# ---- draft create ----------------------------------------------------------


def test_create_draft_bare_dict_no_project(client, workspace):
    r = client.api_post(W(workspace) + "/draft-issues/", json={"name": "Draft " + unique()})
    assert_status(r, 201)
    body = r.json()
    assert isinstance(body, dict)
    assert "results" not in body and "total_count" not in body
    # Exact key set (drift in either direction is a failure).
    assert_fields(body, DRAFT_SHAPE, where="draft.create")
    assert is_uuid(body["id"])
    assert body["project_id"] is None
    assert body["module_ids"] == [] and body["assignee_ids"] == []


def test_create_draft_with_project_and_priority(client, workspace, project):
    r = client.api_post(
        W(workspace) + "/draft-issues/",
        json={"name": "Draft " + unique(), "project_id": project["id"], "priority": "high"},
    )
    assert_status(r, 201)
    body = r.json()
    assert_fields(body, DRAFT_SHAPE, where="draft.create.project")
    assert body["project_id"] == project["id"]
    assert body["priority"] == "high"


def test_create_draft_missing_name_still_201(client, workspace):
    # Unlike core issues, a draft does NOT require a name.
    r = client.api_post(W(workspace) + "/draft-issues/", json={})
    assert_status(r, 201)
    body = r.json()
    assert_fields(body, DRAFT_SHAPE, where="draft.create.noname")
    assert body["name"] is None


def test_create_draft_start_after_target_400(client, workspace, project):
    r = client.api_post(
        W(workspace) + "/draft-issues/",
        json={
            "name": "Draft " + unique(),
            "project_id": project["id"],
            "start_date": "2025-05-10",
            "target_date": "2025-05-01",
        },
    )
    assert_status(r, 400)
    assert "non_field_errors" in r.json()


# ---- draft list / retrieve -------------------------------------------------


def test_list_drafts_returns_envelope(client, workspace, project):
    _make_draft(client, workspace, project)
    r = client.api_get(W(workspace) + "/draft-issues/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="draft.list", grouped=False)
    assert isinstance(body["results"], list)
    assert body["results"], "expected at least one draft row"
    assert_fields(body["results"][0], DRAFT_SHAPE, where="draft.list[0]")


def test_retrieve_own_draft(client, workspace, project):
    draft = _make_draft(client, workspace, project)
    r = client.api_get(W(workspace) + f"/draft-issues/{draft['id']}/")
    assert_status(r, 200)
    assert_fields(r.json(), DRAFT_SHAPE, where="draft.retrieve")


def test_retrieve_missing_draft_404(client, workspace):
    r = client.api_get(W(workspace) + "/draft-issues/00000000-0000-0000-0000-000000000000/")
    assert_status(r, 404)


# ---- draft patch / delete --------------------------------------------------


def test_patch_draft_returns_204_empty_body(client, workspace, project):
    draft = _make_draft(client, workspace, project)
    r = client.api_patch(W(workspace) + f"/draft-issues/{draft['id']}/", json={"name": "Renamed"})
    assert_status(r, 204)
    assert r.text == ""


def test_delete_draft_returns_204(client, workspace, project):
    draft = _make_draft(client, workspace, project)
    r = client.api_delete(W(workspace) + f"/draft-issues/{draft['id']}/")
    assert_status(r, 204)


# ---- draft -> issue --------------------------------------------------------


def test_draft_to_issue_promotes(client, workspace, project):
    draft = _make_draft(client, workspace, project)
    name = "Real " + unique()
    r = client.api_post(W(workspace) + f"/draft-to-issue/{draft['id']}/", json={"name": name})
    assert_status(r, 201)
    body = r.json()
    assert_has_fields(
        body,
        {
            "id": str,
            "name": str,
            "project": str,
            "workspace": str,
            "is_draft": bool,
            "sequence_id": int,
            "created_at": str,
        },
        where="draft.to_issue",
    )
    assert is_uuid(body["id"])
    assert body["name"] == name
    assert body["project"] == project["id"]
    assert body["is_draft"] is False
    # the draft is consumed by promotion.
    assert_status(client.api_get(W(workspace) + f"/draft-issues/{draft['id']}/"), 404)


def test_draft_to_issue_without_project_400(client, workspace):
    draft = _make_draft(client, workspace)  # no project_id
    r = client.api_post(W(workspace) + f"/draft-to-issue/{draft['id']}/", json={"name": "Real " + unique()})
    assert_status(r, 400)


# ---- draft permissions -----------------------------------------------------


def test_non_member_cannot_access_drafts(client, workspace, project, fresh_client):
    base = W(workspace) + "/draft-issues/"
    assert_status(fresh_client.api_get(base), 403)
    assert_status(fresh_client.api_post(base, json={"name": "x " + unique()}), 403)
    draft = _make_draft(client, workspace, project)
    assert_status(fresh_client.api_get(base + f"{draft['id']}/"), 403)


# ---- bulk-update-dates -----------------------------------------------------


def _make_issue(client, workspace, project, name=None):
    r = client.api_post(P(workspace, project) + "/issues/", json={"name": name or ("I " + unique())})
    assert_status(r, 201)
    return r.json()


def test_bulk_update_dates_ok_and_persists(client, workspace, project):
    i1 = _make_issue(client, workspace, project)
    i2 = _make_issue(client, workspace, project)
    r = client.api_post(
        P(workspace, project) + "/issue-dates/",
        json={
            "updates": [
                {"id": i1["id"], "start_date": "2025-01-01", "target_date": "2025-02-01"},
                {"id": i2["id"], "target_date": "2025-03-01"},
            ]
        },
    )
    assert_status(r, 200)
    assert r.json() == {"message": "Issues updated successfully"}
    # dates persisted on i1.
    got = client.api_get(P(workspace, project) + f"/issues/{i1['id']}/")
    assert_status(got, 200)
    assert got.json()["start_date"] == "2025-01-01"
    assert got.json()["target_date"] == "2025-02-01"


def test_bulk_update_dates_start_after_target_400(client, workspace, project):
    i1 = _make_issue(client, workspace, project)
    r = client.api_post(
        P(workspace, project) + "/issue-dates/",
        json={"updates": [{"id": i1["id"], "start_date": "2025-05-01", "target_date": "2025-01-01"}]},
    )
    assert_status(r, 400)
    assert r.json() == {"message": "Start date cannot exceed target date"}


def test_bulk_update_dates_empty_is_noop_200(client, workspace, project):
    r = client.api_post(P(workspace, project) + "/issue-dates/", json={"updates": []})
    assert_status(r, 200)
    assert r.json() == {"message": "Issues updated successfully"}


def test_bulk_update_dates_non_member_403(workspace, project, fresh_client):
    r = fresh_client.api_post(P(workspace, project) + "/issue-dates/", json={"updates": []})
    assert_status(r, 403)
