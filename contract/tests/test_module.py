"""Contract: module endpoints.

Covers create (201, bare `.values()` dict — NOT the full ModuleSerializer), list
(bare list), retrieve (adds distribution/estimate_distribution/link_module/sub_issues),
patch (200), put (200, **different shape** — full ModuleSerializer with
project/workspace/lead/members instead of *_id/computed-stats), delete (204),
duplicate-name validation (400), start/target date validation (400), module-issues
add/list/remove, and the workspace-wide module list (bare list).

QUIRKS pinned here (probed live against the Python reference):
  - POST/GET/PATCH return a bare dict/list of `Module.objects.values(...)` rows —
    no envelope, no nesting.
  - PUT (`updateModule`) returns a *different* serializer shape than POST/GET/PATCH:
    `project`/`workspace`/`lead`/`members`/`created_by`/`updated_by`/`deleted_at`
    instead of `project_id`/`workspace_id`/`is_favorite`/the issue-count fields.
  - `POST .../modules/<id>/issues/` returns `201 {"message": "success"}`, not the
    created objects.
  - The workspace-wide list (`GET /api/workspaces/<slug>/modules/`) is yet a third
    shape: no `is_favorite`, no estimate-points fields, and `member_ids` observed
    as `null` (not `[]`).
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

pytestmark = pytest.mark.module

# Shape returned by POST (create), GET list, GET workspace-list-item's superset,
# and PATCH — a bare `.values()`-style dict (project-scoped).
MODULE_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "description": str,
    "description_text": OPTIONAL(str, dict),
    "description_html": OPTIONAL(str, dict),
    "start_date": OPTIONAL(str),
    "target_date": OPTIONAL(str),
    "status": str,
    "lead_id": OPTIONAL(str),
    "member_ids": OPTIONAL(list),
    "view_props": dict,
    "sort_order": (int, float),
    "external_source": OPTIONAL(str),
    "external_id": OPTIONAL(str),
    "logo_props": dict,
    "created_at": str,
    "updated_at": str,
    "is_favorite": bool,
    "total_issues": int,
    "cancelled_issues": int,
    "completed_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_issues": int,
    "completed_estimate_points": (int, float),
    "total_estimate_points": (int, float),
}

# Retrieve (GET detail) is a superset of MODULE_SHAPE.
MODULE_DETAIL_EXTRA = {
    "archived_at": OPTIONAL(str),
    "link_module": list,
    "sub_issues": int,
    "backlog_estimate_points": (int, float),
    "unstarted_estimate_points": (int, float),
    "started_estimate_points": (int, float),
    "cancelled_estimate_points": (int, float),
    "estimate_distribution": dict,
    "distribution": dict,
}

# PUT (updateModule) is a *different* serializer entirely — full ModuleSerializer.
MODULE_PUT_SHAPE = {
    "id": str,
    "lead_id": OPTIONAL(str),
    "created_at": str,
    "updated_at": str,
    "deleted_at": OPTIONAL(str),
    "name": str,
    "description": str,
    "description_text": OPTIONAL(str, dict),
    "description_html": OPTIONAL(str, dict),
    "start_date": OPTIONAL(str),
    "target_date": OPTIONAL(str),
    "status": str,
    "view_props": dict,
    "sort_order": (int, float),
    "external_source": OPTIONAL(str),
    "external_id": OPTIONAL(str),
    "archived_at": OPTIONAL(str),
    "logo_props": dict,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "project": str,
    "workspace": str,
    "lead": OPTIONAL(str),
    "members": list,
    "member_ids": list,
}

# Workspace-wide list item — a third shape: no is_favorite, no estimate points.
WORKSPACE_MODULE_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "description": str,
    "description_text": OPTIONAL(str, dict),
    "description_html": OPTIONAL(str, dict),
    "start_date": OPTIONAL(str),
    "target_date": OPTIONAL(str),
    "status": str,
    "lead_id": OPTIONAL(str),
    "member_ids": OPTIONAL(list),
    "view_props": dict,
    "sort_order": (int, float),
    "external_source": OPTIONAL(str),
    "external_id": OPTIONAL(str),
    "logo_props": dict,
    "total_issues": int,
    "cancelled_issues": int,
    "completed_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_issues": int,
    "created_at": str,
    "updated_at": str,
    "archived_at": OPTIONAL(str),
}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create(client, workspace, project, **extra) -> dict:
    body = {"name": f"Mod {unique()}", **extra}
    r = client.api_post(_base(workspace, project) + "/modules/", json=body)
    assert_status(r, 201)
    return r.json()


def test_create_module(client, workspace, project):
    body = _create(client, workspace, project)
    assert_has_fields(body, MODULE_SHAPE, where="module")
    assert is_uuid(body["id"])
    assert body["workspace_id"] == workspace["id"]
    assert body["project_id"] == project["id"]
    assert body["total_issues"] == 0
    assert body["is_favorite"] is False


def test_create_module_requires_name(client, workspace, project):
    r = client.api_post(_base(workspace, project) + "/modules/", json={})
    assert_status(r, 400)


def test_list_modules(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(_base(workspace, project) + "/modules/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [m for m in body if m["id"] == created["id"]]
    assert len(mine) == 1
    assert_has_fields(mine[0], MODULE_SHAPE, where="module[0]")


def test_retrieve_module(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/modules/{created['id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, MODULE_SHAPE, where="module.retrieve")
    assert_has_fields(body, MODULE_DETAIL_EXTRA, where="module.retrieve")
    assert body["link_module"] == []
    assert body["sub_issues"] == 0
    assert_has_fields(
        body["distribution"],
        {"assignees": ANY, "labels": ANY, "completion_chart": ANY},
        where="module.retrieve.distribution",
    )


def test_patch_module(client, workspace, project):
    created = _create(client, workspace, project)
    new_name = created["name"] + "-patched"
    r = client.api_patch(
        _base(workspace, project) + f"/modules/{created['id']}/", json={"name": new_name}
    )
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, MODULE_SHAPE, where="module.patch")
    assert body["name"] == new_name
    assert body["id"] == created["id"]


def test_put_module(client, workspace, project):
    created = _create(client, workspace, project)
    new_name = created["name"] + "-put"
    r = client.api_put(
        _base(workspace, project) + f"/modules/{created['id']}/", json={"name": new_name}
    )
    assert_status(r, 200)
    body = r.json()
    # PUT deliberately returns a different serializer shape than POST/GET/PATCH.
    assert_has_fields(body, MODULE_PUT_SHAPE, where="module.put")
    assert body["name"] == new_name
    assert body["id"] == created["id"]


def test_delete_module(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_delete(_base(workspace, project) + f"/modules/{created['id']}/")
    assert_status(r, 204)
    r2 = client.api_get(_base(workspace, project) + f"/modules/{created['id']}/")
    assert_status(r2, 404)


def test_duplicate_module_name_rejected(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_post(
        _base(workspace, project) + "/modules/", json={"name": created["name"]}
    )
    assert_status(r, 400)
    assert "error" in r.json()


def test_start_date_after_target_date_rejected(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/modules/",
        json={
            "name": f"Mod DV {unique()}",
            "start_date": "2026-02-10",
            "target_date": "2026-02-01",
        },
    )
    assert_status(r, 400)
    assert "non_field_errors" in r.json()


def test_workspace_wide_modules_list(client, workspace, project):
    created = _create(client, workspace, project)
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/modules/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [m for m in body if m["id"] == created["id"]]
    assert len(mine) == 1
    assert_has_fields(mine[0], WORKSPACE_MODULE_SHAPE, where="workspace_module[0]")


def test_add_and_list_module_issues(client, workspace, project):
    module = _create(client, workspace, project)
    ri = client.api_post(
        _base(workspace, project) + "/issues/", json={"name": f"Issue {unique()}"}
    )
    assert_status(ri, 201)
    issue_id = ri.json()["id"]

    # Empty envelope before any issue is linked.
    r_empty = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/issues/")
    assert_status(r_empty, 200)
    assert_envelope(r_empty.json(), where="module_issues.empty")
    assert r_empty.json()["total_count"] == 0

    r_add = client.api_post(
        _base(workspace, project) + f"/modules/{module['id']}/issues/",
        json={"issues": [issue_id]},
    )
    assert_status(r_add, 201)
    assert r_add.json() == {"message": "success"}

    r_list = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/issues/")
    assert_status(r_list, 200)
    body = r_list.json()
    assert_envelope(body, where="module_issues.populated")
    ids = [i["id"] for i in body["results"]]
    assert issue_id in ids
    linked = next(i for i in body["results"] if i["id"] == issue_id)
    assert_has_fields(
        linked, {"id": str, "name": str, "module_ids": list}, where="module_issues.item"
    )
    assert module["id"] in linked["module_ids"]


def test_remove_module_issue(client, workspace, project):
    module = _create(client, workspace, project)
    ri = client.api_post(
        _base(workspace, project) + "/issues/", json={"name": f"Issue {unique()}"}
    )
    assert_status(ri, 201)
    issue_id = ri.json()["id"]

    r_add = client.api_post(
        _base(workspace, project) + f"/modules/{module['id']}/issues/",
        json={"issues": [issue_id]},
    )
    assert_status(r_add, 201)

    r_del = client.api_delete(
        _base(workspace, project) + f"/modules/{module['id']}/issues/{issue_id}/"
    )
    assert_status(r_del, 204)

    r_list = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/issues/")
    assert_status(r_list, 200)
    body = r_list.json()
    ids = [i["id"] for i in body["results"]]
    assert issue_id not in ids
