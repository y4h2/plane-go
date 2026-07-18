"""Contract: intake / triage inbox endpoints.

Intake is the inbox where incoming work items land for triage. Confirmed against
the live Python reference:

- An intake is NOT created when a project is created. It is auto-created (a single
  default intake, `is_default=true`, named "<project name> Intake") the first time
  the project is PATCHed with `inbox_view: true`. The `intake_project` fixture does
  this so both servers have an intake to work against.
- GET /intakes/ returns a **single object** (not a list) via IntakeSerializer
  (project_detail + pending_issue_count annotations).
- GET /intake-issues/ returns the cursor-pagination **envelope**. By default it is
  filtered to `status=-2` (pending) -- `?status=<n>` overrides.
- POST /intake-issues/ takes a nested `{"issue": {...}}` payload, creates the
  underlying issue (in the project's TRIAGE state) AND the intake_issue row, and
  returns **200** (IntakeIssueDetailSerializer). Missing name -> 400 "Name is
  required"; bad priority -> 400 "Invalid priority".
- status is an int enum: -2 pending, -1 rejected, 0 snoozed, 1 accepted, 2 duplicate.
- PATCH status=1 (accept) transitions the underlying issue out of TRIAGE into the
  project's default state (state_id changes).
- DELETE returns 204 and (for non-accepted statuses) also deletes the issue.
- retrieve/patch/delete of an unknown work item -> 404.
"""

import uuid

import pytest

from lib.client import unique
from lib.shape import (
    ANY,
    OPTIONAL,
    assert_envelope,
    assert_has_fields,
    assert_status,
    is_uuid,
)

pytestmark = pytest.mark.intake


def B(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


# IssueIntakeSerializer -- the compact issue embedded in intake-issue LIST rows.
LIST_ISSUE_SHAPE = {
    "id": str,
    "name": str,
    "priority": str,
    "sequence_id": int,
    "project_id": str,
    "created_at": str,
    "label_ids": list,
    "created_by": OPTIONAL(str),
}

# IssueDetailSerializer subset -- the issue embedded in create/retrieve/patch.
DETAIL_ISSUE_SHAPE = {
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
    "label_ids": list,
    "assignee_ids": list,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "is_draft": bool,
    "archived_at": OPTIONAL(str),
    "description_html": str,
}

# IntakeIssueSerializer -- the LIST row envelope item.
LIST_ROW_SHAPE = {
    "id": str,
    "status": int,
    "duplicate_to": OPTIONAL(str),
    "snoozed_till": OPTIONAL(str),
    "source": OPTIONAL(str),
    "issue": dict,
    "created_by": OPTIONAL(str),
}

# IntakeIssueDetailSerializer -- create / retrieve / patch response.
DETAIL_SHAPE = {
    "id": str,
    "status": int,
    "duplicate_to": OPTIONAL(str),
    "snoozed_till": OPTIONAL(str),
    "duplicate_issue_detail": ANY,
    "source": OPTIONAL(str),
    "issue": dict,
}


@pytest.fixture
def intake_project(client, workspace, project):
    """A project with its default intake created (via inbox_view PATCH)."""
    r = client.api_patch(B(workspace, project) + "/", json={"inbox_view": True})
    assert_status(r, 200)
    return workspace, project


def _create_intake_issue(client, workspace, project, name=None, priority=None):
    issue = {"name": name or ("Triage " + unique())}
    if priority is not None:
        issue["priority"] = priority
    r = client.api_post(B(workspace, project) + "/intake-issues/", json={"issue": issue})
    assert_status(r, 200)
    return r.json()


# ---- intake object ---------------------------------------------------------


def test_get_intake_returns_single_object(client, intake_project):
    workspace, project = intake_project
    r = client.api_get(B(workspace, project) + "/intakes/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict)
    assert "results" not in body  # single object, not an envelope/list
    assert_has_fields(
        body,
        {
            "id": str,
            "name": str,
            "is_default": bool,
            "pending_issue_count": int,
            "project": str,
            "workspace": str,
        },
        where="intake",
    )
    assert is_uuid(body["id"])
    assert is_uuid(body["project"]) and body["project"] == project["id"]


def test_intake_pending_count_reflects_created_issues(client, intake_project):
    workspace, project = intake_project
    before = client.api_get(B(workspace, project) + "/intakes/").json()["pending_issue_count"]
    _create_intake_issue(client, workspace, project)
    _create_intake_issue(client, workspace, project)
    after = client.api_get(B(workspace, project) + "/intakes/").json()["pending_issue_count"]
    assert after == before + 2


# ---- list ------------------------------------------------------------------


def test_list_intake_issues_empty_envelope(client, intake_project):
    workspace, project = intake_project
    r = client.api_get(B(workspace, project) + "/intake-issues/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="intake-issue.list", grouped=False)
    assert body["results"] == []
    assert body["count"] == 0


def test_list_intake_issues_after_create(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project, name="Listed " + unique())
    r = client.api_get(B(workspace, project) + "/intake-issues/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="intake-issue.list", grouped=False)
    assert body["count"] >= 1
    row = next(x for x in body["results"] if x["id"] == created["id"])
    assert_has_fields(row, LIST_ROW_SHAPE, where="intake-issue.list[row]")
    assert row["status"] == -2
    assert_has_fields(row["issue"], LIST_ISSUE_SHAPE, where="intake-issue.list[row].issue")


# ---- create ----------------------------------------------------------------


def test_create_returns_detail_shape(client, intake_project):
    workspace, project = intake_project
    name = "Created " + unique()
    body = _create_intake_issue(client, workspace, project, name=name)
    assert_has_fields(body, DETAIL_SHAPE, where="intake-issue.create")
    assert body["status"] == -2  # pending is the default
    assert body["source"] == "IN_APP"
    assert is_uuid(body["id"])
    assert_has_fields(body["issue"], DETAIL_ISSUE_SHAPE, where="intake-issue.create.issue")
    assert body["issue"]["name"] == name
    # underlying issue lands in a (triage) state
    assert is_uuid(body["issue"]["state_id"])


def test_create_with_priority(client, intake_project):
    workspace, project = intake_project
    body = _create_intake_issue(client, workspace, project, priority="high")
    assert body["issue"]["priority"] == "high"


def test_create_missing_name_400(client, intake_project):
    workspace, project = intake_project
    r = client.api_post(B(workspace, project) + "/intake-issues/", json={"issue": {}})
    assert_status(r, 400)
    assert r.json() == {"error": "Name is required"}


def test_create_missing_issue_key_400(client, intake_project):
    workspace, project = intake_project
    r = client.api_post(B(workspace, project) + "/intake-issues/", json={})
    assert_status(r, 400)
    assert r.json() == {"error": "Name is required"}


def test_create_invalid_priority_400(client, intake_project):
    workspace, project = intake_project
    r = client.api_post(
        B(workspace, project) + "/intake-issues/",
        json={"issue": {"name": "x " + unique(), "priority": "bogus"}},
    )
    assert_status(r, 400)
    assert r.json() == {"error": "Invalid priority"}


# ---- retrieve --------------------------------------------------------------


def test_retrieve_intake_issue(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    r = client.api_get(B(workspace, project) + f"/intake-issues/{issue_id}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, DETAIL_SHAPE, where="intake-issue.retrieve")
    assert body["id"] == created["id"]
    assert_has_fields(body["issue"], DETAIL_ISSUE_SHAPE, where="intake-issue.retrieve.issue")


def test_retrieve_nonexistent_404(client, intake_project):
    workspace, project = intake_project
    r = client.api_get(B(workspace, project) + f"/intake-issues/{uuid.uuid4()}/")
    assert_status(r, 404)


# ---- triage status transitions --------------------------------------------


def test_accept_transitions_issue_state(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    before_state = created["issue"]["state_id"]
    r = client.api_patch(
        B(workspace, project) + f"/intake-issues/{issue_id}/", json={"status": 1}
    )
    assert_status(r, 200)
    body = r.json()
    assert body["status"] == 1
    after_state = body["issue"]["state_id"]
    assert is_uuid(after_state)
    # accepting moves the issue out of the triage state into the default state
    assert after_state != before_state


def test_reject(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    r = client.api_patch(
        B(workspace, project) + f"/intake-issues/{issue_id}/", json={"status": -1}
    )
    assert_status(r, 200)
    assert r.json()["status"] == -1


def test_snooze(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    r = client.api_patch(
        B(workspace, project) + f"/intake-issues/{issue_id}/",
        json={"status": 0, "snoozed_till": "2030-01-01T00:00:00Z"},
    )
    assert_status(r, 200)
    body = r.json()
    assert body["status"] == 0
    assert body["snoozed_till"] is not None


def test_mark_duplicate(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    r = client.api_patch(
        B(workspace, project) + f"/intake-issues/{issue_id}/", json={"status": 2}
    )
    assert_status(r, 200)
    assert r.json()["status"] == 2


def test_patch_nonexistent_404(client, intake_project):
    workspace, project = intake_project
    r = client.api_patch(
        B(workspace, project) + f"/intake-issues/{uuid.uuid4()}/", json={"status": 1}
    )
    assert_status(r, 404)


# ---- status filter ---------------------------------------------------------


def test_status_filter(client, intake_project):
    workspace, project = intake_project
    # one accepted, one left pending
    accepted = _create_intake_issue(client, workspace, project)
    client.api_patch(
        B(workspace, project) + f"/intake-issues/{accepted['issue']['id']}/",
        json={"status": 1},
    )
    _create_intake_issue(client, workspace, project)  # stays pending (-2)

    r = client.api_get(B(workspace, project) + "/intake-issues/?status=1")
    assert_status(r, 200)
    rows = r.json()["results"]
    assert rows, "expected at least the accepted row"
    assert all(row["status"] == 1 for row in rows)
    assert any(row["id"] == accepted["id"] for row in rows)


# ---- delete ----------------------------------------------------------------


def test_delete_intake_issue(client, intake_project):
    workspace, project = intake_project
    created = _create_intake_issue(client, workspace, project)
    issue_id = created["issue"]["id"]
    r = client.api_delete(B(workspace, project) + f"/intake-issues/{issue_id}/")
    assert_status(r, 204)
    assert r.text == ""
    # gone afterwards
    r2 = client.api_get(B(workspace, project) + f"/intake-issues/{issue_id}/")
    assert_status(r2, 404)


def test_delete_nonexistent_404(client, intake_project):
    workspace, project = intake_project
    r = client.api_delete(B(workspace, project) + f"/intake-issues/{uuid.uuid4()}/")
    assert_status(r, 404)
