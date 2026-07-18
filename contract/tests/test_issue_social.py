"""Contract: issue subscribe/unsubscribe, subscribers list, and reactions."""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.issue_social


def _issue(client, workspace, project) -> str:
    base = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"
    r = client.api_post(base + "/issues/", json={"name": "I " + unique()})
    assert_status(r, 201)
    return r.json()["id"]


def _base(client, workspace, project, issue_id) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/issues/{issue_id}"


def test_subscribe_status_is_bool(client, workspace, project):
    b = _base(client, workspace, project, _issue(client, workspace, project))
    r = client.api_get(b + "/subscribe/")
    assert_status(r, 200)
    assert isinstance(r.json()["subscribed"], bool)


def test_subscribers_list(client, workspace, project):
    b = _base(client, workspace, project, _issue(client, workspace, project))
    r = client.api_get(b + "/issue-subscribers/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_add_and_list_reaction(client, workspace, project):
    b = _base(client, workspace, project, _issue(client, workspace, project))
    assert client.api_get(b + "/reactions/").json() == [] or isinstance(client.api_get(b + "/reactions/").json(), list)
    r = client.api_post(b + "/reactions/", json={"reaction": "128077"})
    assert_status(r, 201)
    body = r.json()
    assert is_uuid(body["id"])
    assert body["reaction"] == "128077"
    rl = client.api_get(b + "/reactions/")
    assert_status(rl, 200)
    assert any(x["id"] == body["id"] for x in rl.json())
