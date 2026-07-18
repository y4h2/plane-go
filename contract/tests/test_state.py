"""Contract: state endpoints.

Covers list (bare + ?grouped=true), create (**200, not 201** — deliberate quirk),
retrieve, partial update, mark-default (204), delete (204 normally; 400 when the
state is the project's default or still has issues on it), the workspace-wide
state list, and the "triage" group being forbidden on create.

Every fresh project auto-provisions 5 default states, one per group: backlog,
unstarted, started, completed, cancelled (the `backlog` one is `default: true`).
Every fresh *workspace* also auto-provisions a hidden onboarding project (named
after the workspace) with its own 5 default states — so the workspace-wide list
is never scoped to just the project under test; assertions there check for a
superset, not an exact match.

Confirmed field-shape quirk: create/retrieve/patch responses carry 9 fields
(no `order`); list responses (bare and grouped, project-scoped and
workspace-wide) carry those same 9 fields *plus* `order` (float).
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_status, is_uuid

pytestmark = pytest.mark.state

GROUPS = {"backlog", "unstarted", "started", "completed", "cancelled"}

# StateSerializer shape as returned by create/retrieve/patch (no `order`).
STATE_SHAPE = {
    "id": str,
    "project_id": str,
    "workspace_id": str,
    "name": str,
    "color": str,
    "group": str,
    "default": bool,
    "description": str,
    "sequence": float,
}

# Same shape but as returned by list endpoints (bare + grouped): adds `order`.
STATE_LIST_SHAPE = {**STATE_SHAPE, "order": float}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create(client, workspace, project, **overrides) -> dict:
    body = {"name": "St " + unique(), "color": "#112233", "group": "started"}
    body.update(overrides)
    r = client.api_post(_base(workspace, project) + "/states/", json=body)
    assert_status(r, 200)  # quirk: create returns 200, not 201
    return r.json()


def test_list_states_bare(client, workspace, project):
    r = client.api_get(_base(workspace, project) + "/states/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    assert len(body) >= 5
    for s in body:
        assert_fields(s, STATE_LIST_SHAPE, where="state")
        assert s["project_id"] == project["id"]
    groups_seen = {s["group"] for s in body}
    assert GROUPS <= groups_seen
    defaults = [s for s in body if s["default"]]
    assert len(defaults) == 1
    assert defaults[0]["group"] == "backlog"


def test_list_states_grouped(client, workspace, project):
    r = client.api_get(_base(workspace, project) + "/states/?grouped=true")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict)
    assert GROUPS <= set(body.keys())
    for group_name, states in body.items():
        assert isinstance(states, list)
        for s in states:
            assert_fields(s, STATE_LIST_SHAPE, where=f"state[{group_name}]")
            assert s["group"] == group_name


def test_create_state(client, workspace, project):
    body = _create(client, workspace, project, name="St " + unique(), group="started")
    assert_fields(body, STATE_SHAPE, where="state")
    assert is_uuid(body["id"])
    assert body["project_id"] == project["id"]
    assert body["workspace_id"] == workspace["id"]
    assert body["group"] == "started"
    assert body["default"] is False


def test_retrieve_state(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/states/{created['id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, STATE_SHAPE, where="state")
    assert body["id"] == created["id"]
    assert body["name"] == created["name"]


def test_retrieve_state_404(client, workspace, project):
    r = client.api_get(_base(workspace, project) + "/states/00000000-0000-0000-0000-000000000000/")
    assert_status(r, 404)


def test_patch_state(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_patch(
        _base(workspace, project) + f"/states/{created['id']}/",
        json={"name": "Renamed " + unique(), "color": "#abcdef"},
    )
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, STATE_SHAPE, where="state")
    assert body["color"] == "#abcdef"
    assert body["id"] == created["id"]


def test_mark_default_state(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_post(_base(workspace, project) + f"/states/{created['id']}/mark-default/")
    assert_status(r, 204)

    retrieved = client.api_get(_base(workspace, project) + f"/states/{created['id']}/").json()
    assert retrieved["default"] is True


def test_delete_state(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_delete(_base(workspace, project) + f"/states/{created['id']}/")
    assert_status(r, 204)

    r2 = client.api_get(_base(workspace, project) + f"/states/{created['id']}/")
    assert_status(r2, 404)


def test_delete_default_state_forbidden(client, workspace, project):
    created = _create(client, workspace, project)
    mark = client.api_post(_base(workspace, project) + f"/states/{created['id']}/mark-default/")
    assert_status(mark, 204)

    r = client.api_delete(_base(workspace, project) + f"/states/{created['id']}/")
    assert_status(r, 400)


def test_delete_state_with_issues_forbidden(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_post(
        _base(workspace, project) + "/issues/",
        json={"name": "Issue " + unique(), "state": created["id"]},
    )
    assert_status(r, 201)

    r2 = client.api_delete(_base(workspace, project) + f"/states/{created['id']}/")
    assert_status(r2, 400)


def test_create_triage_state_forbidden(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/states/",
        json={"name": "Triage " + unique(), "color": "#112233", "group": "triage"},
    )
    assert_status(r, 400)


def test_create_state_requires_name_and_color(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/states/", json={"color": "#112233", "group": "started"}
    )
    assert_status(r, 400)
    r2 = client.api_post(
        _base(workspace, project) + "/states/", json={"name": "No Color " + unique()}
    )
    assert_status(r2, 400)


def test_create_duplicate_state_name_forbidden(client, workspace, project):
    name = "Dup " + unique()
    r1 = client.api_post(
        _base(workspace, project) + "/states/",
        json={"name": name, "color": "#112233", "group": "started"},
    )
    assert_status(r1, 200)
    r2 = client.api_post(
        _base(workspace, project) + "/states/",
        json={"name": name, "color": "#112233", "group": "started"},
    )
    assert_status(r2, 400)


def test_workspace_wide_list_states(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/states/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    for s in body:
        assert_fields(s, STATE_LIST_SHAPE, where="state")

    ids = {s["id"] for s in body}
    assert created["id"] in ids
    project_ids = {s["project_id"] for s in body}
    assert project["id"] in project_ids
