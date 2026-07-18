"""Contract: issue actions — archive/unarchive, bulk delete, bulk archive,
sub-issues write, issue-relation write (+ inverse + remove), attachments list.

Archive is state-group gated: only issues on a `completed`/`cancelled` group
state may be archived (both the single and bulk endpoints enforce this, with
different error-body shapes — the guard status code is the stable part of the
contract, so the body is only pinned where both endpoints agree).

Relation removal is **not** a DELETE on `issue-relation/`: the app routes it
as `POST .../remove-relation/` (see apps/api/plane/app/urls/issue.py). That is
the endpoint under test here.
"""

import pytest

from lib.client import unique
from lib.shape import assert_status

pytestmark = pytest.mark.issue_actions


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _issue(client, workspace, project, name: str = "I") -> str:
    r = client.api_post(_base(workspace, project) + "/issues/", json={"name": f"{name} " + unique()})
    assert_status(r, 201)
    return r.json()["id"]


def _completed_state_id(client, workspace, project) -> str:
    r = client.api_get(_base(workspace, project) + "/states/")
    assert_status(r, 200)
    completed = [s for s in r.json() if s["group"] == "completed"]
    assert completed, "project has no completed-group state"
    return completed[0]["id"]


def _move_to_completed(client, workspace, project, issue_id: str) -> None:
    r = client.api_patch(
        _base(workspace, project) + f"/issues/{issue_id}/",
        json={"state": _completed_state_id(client, workspace, project)},
    )
    assert_status(r, 200, 204)  # quirk: issue PATCH returns 204 (no body) on this server


# ---- archive / unarchive ---------------------------------------------------


def test_archive_backlog_issue_rejected(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_post(_base(workspace, project) + f"/issues/{iid}/archive/")
    assert_status(r, 400)


def test_archive_completed_issue_succeeds(client, workspace, project):
    iid = _issue(client, workspace, project)
    _move_to_completed(client, workspace, project, iid)

    r = client.api_post(_base(workspace, project) + f"/issues/{iid}/archive/")
    assert_status(r, 200)
    assert "archived_at" in r.json()


def test_unarchive_issue(client, workspace, project):
    iid = _issue(client, workspace, project)
    _move_to_completed(client, workspace, project, iid)
    archived = client.api_post(_base(workspace, project) + f"/issues/{iid}/archive/")
    assert_status(archived, 200)

    r = client.api_delete(_base(workspace, project) + f"/issues/{iid}/archive/")
    assert_status(r, 204)


# ---- bulk delete ------------------------------------------------------------


def test_bulk_delete_issues(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_delete(_base(workspace, project) + "/bulk-delete-issues/", json={"issue_ids": [iid]})
    assert_status(r, 200)
    assert "1" in r.json().get("message", "")

    # gone afterwards
    r2 = client.api_get(_base(workspace, project) + f"/issues/{iid}/")
    assert_status(r2, 404)


# ---- bulk archive -------------------------------------------------------


def test_bulk_archive_backlog_issue_rejected(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_post(_base(workspace, project) + "/bulk-archive-issues/", json={"issue_ids": [iid]})
    assert_status(r, 400)


def test_bulk_archive_completed_issue(client, workspace, project):
    iid = _issue(client, workspace, project)
    _move_to_completed(client, workspace, project, iid)

    r = client.api_post(_base(workspace, project) + "/bulk-archive-issues/", json={"issue_ids": [iid]})
    assert_status(r, 200)
    assert "archived_at" in r.json()


# ---- sub-issues write -----------------------------------------------------


def test_sub_issues_write_and_list(client, workspace, project):
    parent = _issue(client, workspace, project, "parent")
    child = _issue(client, workspace, project, "child")

    r = client.api_post(
        _base(workspace, project) + f"/issues/{parent}/sub-issues/", json={"sub_issue_ids": [child]}
    )
    assert_status(r, 200)
    body = r.json()
    assert "sub_issues" in body
    assert len(body["sub_issues"]) == 1
    assert body["sub_issues"][0]["id"] == child

    r2 = client.api_get(_base(workspace, project) + f"/issues/{parent}/sub-issues/")
    assert_status(r2, 200)
    assert len(r2.json()["sub_issues"]) == 1


# ---- relations write --------------------------------------------------------


def _blocked_by(client, workspace, project, a: str, b: str):
    """Create a `blocked_by` relation A -> B. Returns (a, b)."""
    r = client.api_post(
        _base(workspace, project) + f"/issues/{a}/issue-relation/",
        json={"relation_type": "blocked_by", "issues": [b]},
    )
    assert_status(r, 201)
    return a, b


def test_relations_write(client, workspace, project):
    a = _issue(client, workspace, project, "A")
    b = _issue(client, workspace, project, "B")
    _blocked_by(client, workspace, project, a, b)

    ra = client.api_get(_base(workspace, project) + f"/issues/{a}/issue-relation/")
    assert_status(ra, 200)
    a_rel = ra.json()
    assert isinstance(a_rel, dict)
    assert len(a_rel["blocked_by"]) == 1
    assert a_rel["blocked_by"][0]["id"] == b


def test_relations_inverse_visible(client, workspace, project):
    a = _issue(client, workspace, project, "A")
    b = _issue(client, workspace, project, "B")
    _blocked_by(client, workspace, project, a, b)

    # inverse relation is visible from the other side
    rb = client.api_get(_base(workspace, project) + f"/issues/{b}/issue-relation/")
    assert_status(rb, 200)
    b_rel = rb.json()
    assert len(b_rel["blocking"]) == 1
    assert b_rel["blocking"][0]["id"] == a


def test_relations_remove(client, workspace, project):
    a = _issue(client, workspace, project, "A")
    b = _issue(client, workspace, project, "B")
    _blocked_by(client, workspace, project, a, b)

    # removal is POST .../remove-relation/, not DELETE .../issue-relation/
    rd = client.api_post(
        _base(workspace, project) + f"/issues/{a}/remove-relation/",
        json={"relation_type": "blocked_by", "related_issue": b},
    )
    assert_status(rd, 204)

    ra2 = client.api_get(_base(workspace, project) + f"/issues/{a}/issue-relation/")
    assert_status(ra2, 200)
    assert ra2.json()["blocked_by"] == []


# ---- attachments ------------------------------------------------------------


def test_attachments_list_empty(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/issues/{iid}/issue-attachments/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)
    assert r.json() == []
