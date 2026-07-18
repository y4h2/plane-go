"""Contract: per-user per-project properties (issue-view layout/filters)."""

import pytest

from lib.shape import assert_has_fields, assert_status

pytestmark = pytest.mark.user_properties


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/user-properties/"


def test_get_user_properties(client, workspace, project):
    r = client.api_get(_base(workspace, project))
    assert_status(r, 200)
    assert_has_fields(
        r.json(),
        {"id": str, "display_filters": dict, "display_properties": dict, "filters": dict, "project": str},
        where="user-properties",
    )


def test_patch_user_properties_persists(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_patch(base, json={"display_filters": {"layout": "kanban"}})
    assert_status(r, 200)
    # a subsequent GET reflects the change
    again = client.api_get(base)
    assert_status(again, 200)
    assert again.json()["display_filters"].get("layout") == "kanban"
