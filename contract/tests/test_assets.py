# Copyright (c) 2023-present Plane Software, Inc. and contributors
# SPDX-License-Identifier: AGPL-3.0-only
# See the LICENSE file for details.

"""Contract: v2 asset/file-upload endpoints (signed-upload flow).

Covers the POST-slot / PATCH-confirm contract for the three asset-creation
routes: workspace-scoped, project-scoped, and user-scoped.

The signed upload itself (POSTing bytes to `upload_data.url`) talks to a
storage backend that differs by server and is not reliably reachable from
this host on either one: the Python reference signs a direct MinIO URL on
`localhost:9000` (confirmed unreachable here — connection refused), while the
Go port signs a same-origin proxy path under `localhost/api/assets/v2/upload/`.
Per the assignment, the frozen suite sticks to what's identical on both:
POST returns `{upload_data, asset_id, asset_url}` (200), and PATCH confirms.
`upload_data.url`/`upload_data.fields` are asserted present with the right
*types* only — their exact contents (S3 signing fields vs. an empty dict)
are storage-backend-specific, not part of the shared contract.
"""

import uuid

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.assets

UPLOAD_SLOT_SPEC = {
    "upload_data": dict,
    "asset_id": str,
    # Present on every entity_type, but the Python reference resolves it to
    # null for a couple of entity types whose FileAsset.asset_url property
    # has no case for them (e.g. DRAFT_ISSUE_ATTACHMENT at workspace scope) —
    # so pin "present, str-or-None" rather than a hard str type.
    "asset_url": OPTIONAL(str),
}


def _assert_upload_slot(data: dict, *, where: str) -> str:
    """Assert the {upload_data, asset_id, asset_url} shape; return asset_id."""
    assert_has_fields(data, UPLOAD_SLOT_SPEC, where=where)
    assert is_uuid(data["asset_id"]), f"{where}.asset_id: not a uuid: {data['asset_id']!r}"
    upload_data = data["upload_data"]
    assert_has_fields(upload_data, {"url": str, "fields": dict}, where=f"{where}.upload_data")
    assert upload_data["url"], f"{where}.upload_data.url: empty"
    return data["asset_id"]


def _asset_body(**overrides) -> dict:
    body = {
        "name": f"{unique('ct-')}.png",
        "type": "image/png",
        "size": 1111,
        "entity_type": "DRAFT_ISSUE_DESCRIPTION",
    }
    body.update(overrides)
    return body


# ---- workspace-scoped: POST /api/assets/v2/workspaces/{slug}/ --------------


def test_workspace_asset_post_shape(client, workspace):
    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/", json=_asset_body())
    assert_status(r, 200)
    _assert_upload_slot(r.json(), where="workspace asset post")


def test_workspace_asset_patch_confirm(client, workspace):
    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/", json=_asset_body())
    assert_status(r, 200)
    asset_id = _assert_upload_slot(r.json(), where="workspace asset post")

    r2 = client.api_patch(f"/api/assets/v2/workspaces/{workspace['slug']}/{asset_id}/", json={})
    assert_status(r2, 204)


# ---- project-scoped: POST /api/assets/v2/workspaces/{slug}/projects/{id}/ --


def test_project_asset_post_shape(client, workspace, project):
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/projects/{project['id']}/",
        json=_asset_body(entity_type="ISSUE_ATTACHMENT"),
    )
    assert_status(r, 200)
    _assert_upload_slot(r.json(), where="project asset post")


def test_project_asset_patch_confirm(client, workspace, project):
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/projects/{project['id']}/",
        json=_asset_body(entity_type="ISSUE_ATTACHMENT"),
    )
    assert_status(r, 200)
    asset_id = _assert_upload_slot(r.json(), where="project asset post")

    r2 = client.api_patch(
        f"/api/assets/v2/workspaces/{workspace['slug']}/projects/{project['id']}/{asset_id}/",
        json={},
    )
    assert_status(r2, 204)


# ---- user-scoped: POST /api/assets/v2/user-assets/ --------------------------


def test_user_asset_post_shape(client):
    r = client.api_post("/api/assets/v2/user-assets/", json=_asset_body(entity_type="USER_AVATAR"))
    assert_status(r, 200)
    _assert_upload_slot(r.json(), where="user asset post")


def test_user_asset_patch_confirm(client):
    r = client.api_post("/api/assets/v2/user-assets/", json=_asset_body(entity_type="USER_AVATAR"))
    assert_status(r, 200)
    asset_id = _assert_upload_slot(r.json(), where="user asset post")

    r2 = client.api_patch(f"/api/assets/v2/user-assets/{asset_id}/", json={})
    assert_status(r2, 204)


# ---- contract edges shared by the slot-creation routes -----------------------


def test_workspace_asset_post_rejects_invalid_entity_type(client, workspace):
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/",
        json=_asset_body(entity_type="NOT_A_REAL_ENTITY_TYPE"),
    )
    assert_status(r, 400)
    assert isinstance(r.json().get("error"), str)


def test_workspace_asset_patch_confirm_unknown_asset_404(client, workspace):
    r = client.api_patch(f"/api/assets/v2/workspaces/{workspace['slug']}/{uuid.uuid4()}/", json={})
    assert_status(r, 404)
