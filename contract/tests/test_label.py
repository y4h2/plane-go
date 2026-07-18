"""Contract: label endpoints.

Covers project-scoped issue-labels CRUD (create/list/retrieve/patch/put/delete),
case-insensitive per-project name uniqueness validation, and the workspace-wide
label list. `color` is optional (defaults to empty string when omitted); `name`
is required and rejects blank values.
"""

import uuid

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_status, is_uuid

pytestmark = pytest.mark.label

# LabelSerializer (fields="__all__" minus audit fields the probe didn't show).
LABEL_SHAPE = {
    "parent": OPTIONAL(str),
    "name": str,
    "color": str,
    "id": str,
    "project_id": str,
    "workspace_id": str,
    "sort_order": float,
}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create(client, workspace, project, **overrides) -> dict:
    body = {"name": "L " + unique(), "color": "#abcdef"}
    body.update(overrides)
    r = client.api_post(_base(workspace, project) + "/issue-labels/", json=body)
    assert_status(r, 201)
    return r.json()


def test_create_label(client, workspace, project):
    label = _create(client, workspace, project)
    assert_fields(label, LABEL_SHAPE, where="label")
    assert is_uuid(label["id"])
    assert label["project_id"] == project["id"]
    assert label["workspace_id"] == workspace["id"]
    assert label["parent"] is None


def test_create_label_color_optional(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/issue-labels/",
        json={"name": "NoColor " + unique()},
    )
    assert_status(r, 201)
    assert_fields(r.json(), LABEL_SHAPE, where="label")


def test_create_label_requires_name(client, workspace, project):
    r = client.api_post(_base(workspace, project) + "/issue-labels/", json={"color": "#000000"})
    assert_status(r, 400)
    r_blank = client.api_post(
        _base(workspace, project) + "/issue-labels/", json={"name": "", "color": "#000000"}
    )
    assert_status(r_blank, 400)


def test_create_label_duplicate_name_case_insensitive(client, workspace, project):
    name = "Dup " + unique()
    _create(client, workspace, project, name=name)
    r = client.api_post(
        _base(workspace, project) + "/issue-labels/",
        json={"name": name.upper(), "color": "#222222"},
    )
    assert_status(r, 400)
    assert "LABEL_NAME_ALREADY_EXISTS" in str(r.json())


def test_list_labels_bare_ordered(client, workspace, project):
    created = [_create(client, workspace, project) for _ in range(3)]
    r = client.api_get(_base(workspace, project) + "/issue-labels/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    ids = {label["id"] for label in created}
    mine = [label for label in body if label["id"] in ids]
    assert len(mine) == 3
    for label in mine:
        assert_fields(label, LABEL_SHAPE, where="label[]")
    sort_orders = [label["sort_order"] for label in body]
    assert sort_orders == sorted(sort_orders)


def test_retrieve_label(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/issue-labels/{created['id']}/")
    assert_status(r, 200)
    assert_fields(r.json(), LABEL_SHAPE, where="label")
    assert r.json()["id"] == created["id"]


def test_retrieve_label_missing_404(client, workspace, project):
    r = client.api_get(_base(workspace, project) + f"/issue-labels/{uuid.uuid4()}/")
    assert_status(r, 404)


def test_patch_label(client, workspace, project):
    created = _create(client, workspace, project)
    new_name = created["name"] + "-patched"
    r = client.api_patch(
        _base(workspace, project) + f"/issue-labels/{created['id']}/",
        json={"name": new_name, "color": "#123456"},
    )
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, LABEL_SHAPE, where="label")
    assert body["name"] == new_name
    assert body["color"] == "#123456"


def test_put_label(client, workspace, project):
    created = _create(client, workspace, project)
    new_name = created["name"] + "-put"
    r = client.api_put(
        _base(workspace, project) + f"/issue-labels/{created['id']}/",
        json={"name": new_name, "color": "#654321"},
    )
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, LABEL_SHAPE, where="label")
    assert body["name"] == new_name
    assert body["color"] == "#654321"


def test_delete_label(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_delete(_base(workspace, project) + f"/issue-labels/{created['id']}/")
    assert_status(r, 204)
    r_gone = client.api_get(_base(workspace, project) + f"/issue-labels/{created['id']}/")
    assert_status(r_gone, 404)


def test_workspace_wide_label_list(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/labels/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [label for label in body if label["id"] == created["id"]]
    assert len(mine) == 1
    assert_fields(mine[0], LABEL_SHAPE, where="label[0]")
    assert mine[0]["project_id"] == project["id"]
