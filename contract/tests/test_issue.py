"""Contract: issue endpoints (/api/workspaces/<slug>/projects/<id>/issues/).

Confirmed quirks against the live Python reference server:
- CREATE returns a **single bare `.values()` dict** (NOT a pagination envelope).
- LIST returns the cursor-pagination **envelope**; `?group_by=<field>` makes
  `results` a **dict** (grouped). `group_by=state` is REJECTED (400) -- the
  accepted grouping field is `priority` / `state_id` / `state__group` / etc.
- RETRIEVE (IssueDetailSerializer) adds `description_html` + `is_subscribed`.
  (`is_intake` is documented but NOT emitted by this server -- not asserted.)
- PATCH returns **204 with an EMPTY body** (deliberate quirk).
- DELETE returns 204.
- A non-member of the workspace gets **403** on list/create.
"""

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

pytestmark = pytest.mark.issue


def B(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


# Core bare `.values()` fields shared by create / list-row / retrieve. Counts come
# back null on a fresh issue, so they are OPTIONAL(int); sort_order is a float on
# the wire. NOTE: `deleted_at` is emitted only on CREATE, not on list/retrieve.
ISSUE_SHAPE = {
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
}


def _create_issue(client, workspace, project, name=None):
    r = client.api_post(
        B(workspace, project) + "/issues/",
        json={"name": name or ("Issue " + unique())},
    )
    assert_status(r, 201)
    return r.json()


def test_create_returns_bare_issue_dict(client, workspace, project):
    r = client.api_post(
        B(workspace, project) + "/issues/", json={"name": "Issue " + unique()}
    )
    assert_status(r, 201)
    body = r.json()
    # Bare dict, NOT an envelope.
    assert isinstance(body, dict)
    assert "results" not in body and "total_count" not in body
    assert_has_fields(body, ISSUE_SHAPE, where="issue.create")
    # `deleted_at` is present on CREATE (absent from list/retrieve).
    assert_has_fields(body, {"deleted_at": OPTIONAL(str)}, where="issue.create")
    assert is_uuid(body["id"])
    assert is_uuid(body["project_id"])


def test_create_missing_name_returns_400(client, workspace, project):
    r = client.api_post(B(workspace, project) + "/issues/", json={})
    assert_status(r, 400)
    body = r.json()
    assert "name" in body


def test_create_with_priority(client, workspace, project):
    r = client.api_post(
        B(workspace, project) + "/issues/",
        json={"name": "P " + unique(), "priority": "high"},
    )
    assert_status(r, 201)
    assert r.json()["priority"] == "high"


def test_list_returns_envelope(client, workspace, project):
    _create_issue(client, workspace, project)
    r = client.api_get(B(workspace, project) + "/issues/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="issue.list", grouped=False)
    assert isinstance(body["results"], list)
    if body["results"]:
        assert_has_fields(body["results"][0], ISSUE_SHAPE, where="issue.list[0]")


def test_list_grouped_by_priority_returns_dict_results(client, workspace, project):
    _create_issue(client, workspace, project)
    r = client.api_get(B(workspace, project) + "/issues/?group_by=priority")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="issue.list.grouped", grouped=True)
    assert body["grouped_by"] == "priority"
    assert isinstance(body["results"], dict)


def test_list_grouped_by_invalid_field_returns_400(client, workspace, project):
    # `state` is NOT an accepted group_by field on this server (quirk).
    r = client.api_get(B(workspace, project) + "/issues/?group_by=state")
    assert_status(r, 400)


def test_retrieve_returns_detail_shape(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_get(B(workspace, project) + f"/issues/{issue['id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, ISSUE_SHAPE, where="issue.retrieve")
    # IssueDetailSerializer extras:
    assert_has_fields(
        body,
        {"description_html": str, "is_subscribed": bool},
        where="issue.retrieve.detail",
    )


def test_patch_returns_204_empty_body(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_patch(
        B(workspace, project) + f"/issues/{issue['id']}/", json={"name": "Renamed"}
    )
    # Deliberate quirk: 204 with an empty body -- do NOT try to parse json.
    assert_status(r, 204)
    assert r.text == ""


def test_patch_priority_returns_204(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_patch(
        B(workspace, project) + f"/issues/{issue['id']}/", json={"priority": "urgent"}
    )
    assert_status(r, 204)
    assert r.text == ""


def test_delete_returns_204(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_delete(B(workspace, project) + f"/issues/{issue['id']}/")
    assert_status(r, 204)


def test_list_endpoint_returns_bare_list(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_get(B(workspace, project) + f"/issues/list/?issues={issue['id']}")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)  # bare list, NOT an envelope
    assert any(row["id"] == issue["id"] for row in body)
    if body:
        assert_has_fields(body[0], ISSUE_SHAPE, where="issue.list-endpoint[0]")


def test_non_member_cannot_list_or_create(client, workspace, project, fresh_client):
    # `fresh_client` is a signed-in user who is NOT a member of this workspace.
    base = B(workspace, project)
    assert_status(fresh_client.api_get(base + "/issues/"), 403)
    assert_status(
        fresh_client.api_post(base + "/issues/", json={"name": "x " + unique()}), 403
    )
