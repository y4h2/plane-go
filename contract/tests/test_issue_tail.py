"""Contract: issue tail -- deleted-issues list + work-item-by-identifier lookup.

Confirmed quirks against the live Python reference server:

Deleted-issues (GET /api/workspaces/<slug>/projects/<id>/deleted-issues/):
- Returns a **bare list of issue-id strings** (NOT the cursor envelope, NOT
  `.values()` dicts) -- `Issue.all_objects` filtered to
  `archived_at is not null OR deleted_at is not null`.
- Ordered **newest-created first** (`-created_at`), independent of delete order.
- Accepts an optional `?updated_at__gt=<iso8601>` filter.
- Permission runs BEFORE any object lookup (DRF `allow_permission`, PROJECT
  level, ADMIN/MEMBER/GUEST all allowed -- i.e. any active project member):
  a non-member, an unknown workspace slug, AND an unknown project id all
  yield the SAME **403** `{"error": "You don't have the required
  permissions."}` (never 404).

Work-item-by-identifier (GET /api/workspaces/<slug>/work-items/<PROJECT-N>/):
- `<PROJECT-N>` splits on the LAST `-` into project identifier + sequence
  number; the sequence half must be a plain (optionally `-`-prefixed) integer
  string or the response is **400** `{"error": "Invalid issue identifier"}`.
- Project identifier match is case-INSENSITIVE (`identifier__iexact`).
- On success: 200 with the **full IssueDetailSerializer shape** -- the same
  keys as the plain issue `.values()` dict PLUS `description_html`,
  `is_subscribed`, `is_intake`, and MINUS `deleted_at` (not present here).
  Count fields (`sub_issues_count`/`attachment_count`/`link_count`) come back
  as real **ints (0 default)**, NOT null like the bare `.values()` dict.
- An unknown workspace slug, unknown project identifier, unknown sequence
  number, or a soft-deleted issue's sequence number are all **404**
  `{"error": "The required object does not exist."}` (object lookup, not the
  role-based permission decorator).
- A caller who is not a member of the issue's project (workspace membership
  is NOT required/checked) gets **403** `{"error": "You are not allowed to
  view this issue"}` (note: no trailing period, unlike other error strings).
"""

import uuid

import pytest

from lib.client import PlaneClient, unique
from lib.shape import (
    ANY,
    OPTIONAL,
    assert_has_fields,
    assert_status,
    is_uuid,
)

pytestmark = pytest.mark.issue_tail


def B(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create_issue(client, workspace, project, name=None):
    r = client.api_post(
        B(workspace, project) + "/issues/",
        json={"name": name or ("Issue " + unique())},
    )
    assert_status(r, 201)
    return r.json()


# Full IssueDetailSerializer shape returned by the identifier-lookup endpoint.
# Same core fields as the bare issue `.values()` dict, minus `deleted_at`,
# plus description_html/is_subscribed/is_intake. Counts are real ints here
# (0 default), not the OPTIONAL(int)-nullable counts of the bare dict.
IDENTIFIER_DETAIL_SHAPE = {
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
    "sub_issues_count": int,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "attachment_count": int,
    "link_count": int,
    "is_draft": bool,
    "archived_at": OPTIONAL(str),
    "description_html": str,
    "is_subscribed": bool,
    "is_intake": bool,
}


# ---- deleted-issues ---------------------------------------------------------


def test_deleted_issues_empty_initially(client, workspace, project):
    r = client.api_get(B(workspace, project) + "/deleted-issues/")
    assert_status(r, 200)
    assert r.json() == []


def test_deleted_issues_lists_soft_deleted_issue(client, workspace, project):
    issue = _create_issue(client, workspace, project)

    r = client.api_delete(B(workspace, project) + f"/issues/{issue['id']}/")
    assert_status(r, 204)

    r = client.api_get(B(workspace, project) + "/deleted-issues/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list), f"want bare list, got {type(body).__name__}: {body!r}"
    assert all(isinstance(x, str) and is_uuid(x) for x in body), body
    assert issue["id"] in body


def test_deleted_issues_ordered_newest_created_first(client, workspace, project):
    ids = [_create_issue(client, workspace, project)["id"] for _ in range(3)]
    # Delete out of creation order to prove ordering tracks created_at, not
    # deletion order.
    for idx in (2, 0, 1):
        r = client.api_delete(B(workspace, project) + f"/issues/{ids[idx]}/")
        assert_status(r, 204)

    r = client.api_get(B(workspace, project) + "/deleted-issues/")
    assert_status(r, 200)
    body = r.json()
    positions = [body.index(i) for i in ids]
    # Newest created (ids[2]) should sort before ids[1], which sorts before
    # ids[0] -- i.e. strictly descending creation order.
    assert positions[2] < positions[1] < positions[0], (ids, body)


def test_deleted_issues_updated_at_gt_filter_excludes_old_cutoff(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_delete(B(workspace, project) + f"/issues/{issue['id']}/")
    assert_status(r, 204)

    # A cutoff far in the past includes the issue.
    r = client.api_get(B(workspace, project) + "/deleted-issues/?updated_at__gt=2000-01-01T00:00:00Z")
    assert_status(r, 200)
    assert issue["id"] in r.json()

    # A cutoff far in the future excludes it.
    r = client.api_get(B(workspace, project) + "/deleted-issues/?updated_at__gt=2999-01-01T00:00:00Z")
    assert_status(r, 200)
    assert issue["id"] not in r.json()


def test_deleted_issues_non_member_is_403(fresh_client, workspace, project):
    r = fresh_client.api_get(B(workspace, project) + "/deleted-issues/")
    assert_status(r, 403)
    assert r.json() == {"error": "You don't have the required permissions."}


def test_deleted_issues_unknown_workspace_slug_is_403_not_404(client, project):
    r = client.api_get(f"/api/workspaces/bogus-{unique()}/projects/{project['id']}/deleted-issues/")
    assert_status(r, 403)
    assert r.json() == {"error": "You don't have the required permissions."}


def test_deleted_issues_unknown_project_id_is_403_not_404(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/projects/{uuid.uuid4()}/deleted-issues/")
    assert_status(r, 403)
    assert r.json() == {"error": "You don't have the required permissions."}


# ---- work-items/<PROJECT-N>/ (identifier lookup) ----------------------------


def _identifier_url(workspace, project, sequence_id) -> str:
    return f"/api/workspaces/{workspace['slug']}/work-items/{project['identifier']}-{sequence_id}/"


def test_identifier_lookup_returns_full_detail_shape(client, workspace, project):
    issue = _create_issue(client, workspace, project)

    r = client.api_get(_identifier_url(workspace, project, issue["sequence_id"]))
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, IDENTIFIER_DETAIL_SHAPE, where="work-items")
    assert "deleted_at" not in body, f"identifier lookup should not carry deleted_at: {sorted(body)}"
    assert body["id"] == issue["id"]
    assert body["sequence_id"] == issue["sequence_id"]
    # Fresh issue: annotated counts default to 0 (int), not null.
    assert body["sub_issues_count"] == 0
    assert body["attachment_count"] == 0
    assert body["link_count"] == 0
    assert body["is_subscribed"] is False
    assert body["is_intake"] is False


def test_identifier_lookup_is_case_insensitive_on_project_identifier(client, workspace, project):
    issue = _create_issue(client, workspace, project)

    r = client.api_get(
        f"/api/workspaces/{workspace['slug']}/work-items/{project['identifier'].lower()}-{issue['sequence_id']}/"
    )
    assert_status(r, 200)
    assert r.json()["id"] == issue["id"]


def test_identifier_lookup_unknown_sequence_is_404(client, workspace, project):
    _create_issue(client, workspace, project)
    r = client.api_get(_identifier_url(workspace, project, 999999))
    assert_status(r, 404)
    assert r.json() == {"error": "The required object does not exist."}


def test_identifier_lookup_non_numeric_sequence_is_400(client, workspace, project):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/work-items/{project['identifier']}-abc/")
    assert_status(r, 400)
    assert r.json() == {"error": "Invalid issue identifier"}


def test_identifier_lookup_unknown_project_identifier_is_404(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/work-items/ZZZZZZZZ-1/")
    assert_status(r, 404)
    assert r.json() == {"error": "The required object does not exist."}


def test_identifier_lookup_unknown_workspace_slug_is_404(client, project):
    r = client.api_get(f"/api/workspaces/bogus-{unique()}/work-items/{project['identifier']}-1/")
    assert_status(r, 404)
    assert r.json() == {"error": "The required object does not exist."}


def test_identifier_lookup_soft_deleted_issue_is_404(client, workspace, project):
    issue = _create_issue(client, workspace, project)
    r = client.api_delete(B(workspace, project) + f"/issues/{issue['id']}/")
    assert_status(r, 204)

    r = client.api_get(_identifier_url(workspace, project, issue["sequence_id"]))
    assert_status(r, 404)
    assert r.json() == {"error": "The required object does not exist."}


def test_identifier_lookup_non_project_member_gets_403(client, fresh_client, workspace, project):
    issue = _create_issue(client, workspace, project)

    r = fresh_client.api_get(_identifier_url(workspace, project, issue["sequence_id"]))
    assert_status(r, 403)
    assert r.json() == {"error": "You are not allowed to view this issue"}
