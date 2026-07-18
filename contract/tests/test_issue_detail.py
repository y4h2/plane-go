"""Contract: issue detail sub-resources — comments, links, sub-issues.

(Issue activity/history 500s even on the Python reference, so it is out of scope.)
"""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.issue_detail

COMMENT_SHAPE = {
    "id": str,
    "comment_html": str,
    "comment_stripped": str,
    "actor": str,
    "issue": str,
    "project": str,
    "workspace": str,
    "created_at": str,
}

LINK_SHAPE = {
    "id": str,
    "url": str,
    "title": str,
    "issue": str,
    "created_by": (str, type(None)),
    "created_at": str,
}


def _issue(client, workspace, project) -> str:
    base = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"
    r = client.api_post(base + "/issues/", json={"name": "I " + unique()})
    assert_status(r, 201)
    return r.json()["id"]


def _base(client, workspace, project, issue_id) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/issues/{issue_id}"


def test_add_and_list_comment(client, workspace, project):
    iid = _issue(client, workspace, project)
    b = _base(client, workspace, project, iid)
    r = client.api_post(b + "/comments/", json={"comment_html": "<p>hello</p>"})
    assert_status(r, 201)
    comment = r.json()
    assert_has_fields(comment, COMMENT_SHAPE, where="comment")
    assert is_uuid(comment["id"])
    rl = client.api_get(b + "/comments/")
    assert_status(rl, 200)
    assert any(c["id"] == comment["id"] for c in rl.json())


def test_delete_comment(client, workspace, project):
    iid = _issue(client, workspace, project)
    b = _base(client, workspace, project, iid)
    cid = client.api_post(b + "/comments/", json={"comment_html": "<p>x</p>"}).json()["id"]
    assert_status(client.api_delete(b + f"/comments/{cid}/"), 204)


def test_add_and_list_link(client, workspace, project):
    iid = _issue(client, workspace, project)
    b = _base(client, workspace, project, iid)
    r = client.api_post(b + "/issue-links/", json={"url": "https://example.com", "title": "Docs"})
    assert_status(r, 201)
    link = r.json()
    assert_has_fields(link, LINK_SHAPE, where="link")
    assert link["url"] == "https://example.com"
    rl = client.api_get(b + "/issue-links/")
    assert_status(rl, 200)
    assert any(l["id"] == link["id"] for l in rl.json())


def test_delete_link(client, workspace, project):
    iid = _issue(client, workspace, project)
    b = _base(client, workspace, project, iid)
    lid = client.api_post(b + "/issue-links/", json={"url": "https://a.co", "title": "t"}).json()["id"]
    assert_status(client.api_delete(b + f"/issue-links/{lid}/"), 204)


def test_sub_issues_shape(client, workspace, project):
    iid = _issue(client, workspace, project)
    b = _base(client, workspace, project, iid)
    r = client.api_get(b + "/sub-issues/")
    assert_status(r, 200)
    assert_has_fields(r.json(), {"sub_issues": list, "state_distribution": dict}, where="sub-issues")
