# Copyright (c) 2023-present Plane Software, Inc. and contributors
# SPDX-License-Identifier: AGPL-3.0-only
# See the LICENSE file for details.

"""Contract: v2 asset restore + duplicate.

Covers:
  - POST /api/assets/v2/workspaces/{slug}/restore/{asset_id}/
  - POST /api/assets/v2/workspaces/{slug}/duplicate-assets/{asset_id}/

Restore un-deletes a soft-deleted asset (FileAsset.all_objects lookup, clears
is_deleted/deleted_at unconditionally) -> 204, no body. It's a no-op (still
204) when called on an asset that was never deleted -- confirmed against the
Python reference -- and 404 with a generic error envelope when the asset
doesn't exist in that workspace at all.

Duplicate creates a new FileAsset row copying the source's name/type/size,
then synchronously copies the underlying object in the storage backend
before returning `{"asset_id": <new uuid>}` (200). The synchronous storage
copy is backend-specific and, like the signed-upload-URL flow covered in
test_assets.py, isn't reliably reachable from this host on the Python
reference (object storage's port isn't published to the host in this
environment) -- so the *validation* paths (400/404, none of which reach the
storage call) are frozen tightly, while the success path tolerates either a
200 (storage reachable / local-disk store) or a 500 storage-backend failure,
matching the precedent set in test_assets.py for the analogous upload issue.
"""

import uuid

import pytest
import requests

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.asset_extras


def _asset_body(**overrides) -> dict:
    body = {
        "name": f"{unique('ct-')}.png",
        "type": "image/png",
        "size": 1111,
        "entity_type": "DRAFT_ISSUE_DESCRIPTION",
    }
    body.update(overrides)
    return body


def _create_confirmed_asset(client, slug, **body_overrides) -> str:
    """Create + PATCH-confirm a workspace-scoped asset; return its asset_id."""
    r = client.api_post(f"/api/assets/v2/workspaces/{slug}/", json=_asset_body(**body_overrides))
    assert_status(r, 200)
    asset_id = r.json()["asset_id"]
    r2 = client.api_patch(f"/api/assets/v2/workspaces/{slug}/{asset_id}/", json={})
    assert_status(r2, 204)
    return asset_id


# ---- restore -----------------------------------------------------------------


def test_restore_previously_deleted_asset(client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])

    r_del = client.api_delete(f"/api/assets/v2/workspaces/{workspace['slug']}/{asset_id}/")
    assert_status(r_del, 204)

    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/restore/{asset_id}/")
    assert_status(r, 204)
    assert not r.text


def test_restore_is_idempotent_noop_on_non_deleted_asset(client, workspace):
    """Restoring an asset that was never deleted still 204s (confirmed on the
    Python reference: it unconditionally clears the deleted marker rather
    than requiring the asset to currently be in a deleted state)."""
    asset_id = _create_confirmed_asset(client, workspace["slug"])

    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/restore/{asset_id}/")
    assert_status(r, 204)

    r2 = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/restore/{asset_id}/")
    assert_status(r2, 204)


def test_restore_unknown_asset_404(client, workspace):
    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/restore/{uuid.uuid4()}/")
    assert_status(r, 404)
    assert isinstance(r.json().get("error"), str)


def test_restore_wrong_workspace_404(client, workspace):
    """An asset in workspace A isn't restorable through workspace B's slug."""
    other_ws = client.create_workspace()
    asset_id = _create_confirmed_asset(client, workspace["slug"])

    r = client.api_post(f"/api/assets/v2/workspaces/{other_ws['slug']}/restore/{asset_id}/")
    assert_status(r, 404)
    assert isinstance(r.json().get("error"), str)


def test_restore_unauthenticated_401(base_url, client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])
    r = requests.post(
        f"{base_url}/api/assets/v2/workspaces/{workspace['slug']}/restore/{asset_id}/",
        timeout=30,
    )
    assert_status(r, 401)
    assert r.json() == {"detail": "Authentication credentials were not provided."}


# ---- duplicate -----------------------------------------------------------------


def test_duplicate_rejects_missing_entity_type(client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={},
    )
    assert_status(r, 400)
    assert isinstance(r.json().get("error"), str)


def test_duplicate_rejects_invalid_entity_type(client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={"entity_type": "NOT_A_REAL_ENTITY_TYPE"},
    )
    assert_status(r, 400)
    assert isinstance(r.json().get("error"), str)


def test_duplicate_unknown_asset_404(client, workspace):
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{uuid.uuid4()}/",
        json={"entity_type": "DRAFT_ISSUE_DESCRIPTION"},
    )
    assert_status(r, 404)
    assert isinstance(r.json().get("error"), str)


def test_duplicate_wrong_workspace_404(client, workspace):
    other_ws = client.create_workspace()
    asset_id = _create_confirmed_asset(client, workspace["slug"])

    r = client.api_post(
        f"/api/assets/v2/workspaces/{other_ws['slug']}/duplicate-assets/{asset_id}/",
        json={"entity_type": "DRAFT_ISSUE_DESCRIPTION"},
    )
    assert_status(r, 404)
    assert isinstance(r.json().get("error"), str)


def test_duplicate_unknown_project_404(client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])
    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={"entity_type": "PROJECT_COVER", "project_id": str(uuid.uuid4())},
    )
    assert_status(r, 404)
    assert isinstance(r.json().get("error"), str)


def test_duplicate_source_not_uploaded_404(client, workspace):
    """The source asset must have completed the PATCH-confirm step; a bare
    upload slot (never confirmed) isn't a valid duplication source."""
    r = client.api_post(f"/api/assets/v2/workspaces/{workspace['slug']}/", json=_asset_body())
    assert_status(r, 200)
    unconfirmed_id = r.json()["asset_id"]

    r2 = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{unconfirmed_id}/",
        json={"entity_type": "DRAFT_ISSUE_DESCRIPTION"},
    )
    assert_status(r2, 404)
    assert isinstance(r2.json().get("error"), str)


def test_duplicate_unauthenticated_401(base_url, client, workspace):
    asset_id = _create_confirmed_asset(client, workspace["slug"])
    r = requests.post(
        f"{base_url}/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={"entity_type": "DRAFT_ISSUE_DESCRIPTION"},
        timeout=30,
    )
    assert_status(r, 401)
    assert r.json() == {"detail": "Authentication credentials were not provided."}


def test_duplicate_success_shape_or_storage_unavailable(client, workspace, project):
    """Happy path: valid source asset + valid entity_type (+ optional
    project_id/entity_id). See module docstring for why the status here
    isn't pinned to a single value."""
    asset_id = _create_confirmed_asset(
        client, workspace["slug"], entity_type="PROJECT_COVER", entity_identifier=project["id"]
    )

    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={
            "entity_type": "PROJECT_COVER",
            "project_id": project["id"],
            "entity_id": project["id"],
        },
    )
    if r.status_code == 200:
        data = r.json()
        assert_has_fields(data, {"asset_id": str}, where="duplicate")
        assert is_uuid(data["asset_id"]), f"duplicate.asset_id: not a uuid: {data['asset_id']!r}"
        assert data["asset_id"] != asset_id, "duplicate should mint a new asset id"
    else:
        assert_status(r, 500)
        assert isinstance(r.json().get("error"), str)


def test_duplicate_success_without_project(client, workspace):
    """entity_type alone (no project_id/entity_id) is a valid duplicate
    request -- workspace-level entity types have no project scoping."""
    asset_id = _create_confirmed_asset(client, workspace["slug"])

    r = client.api_post(
        f"/api/assets/v2/workspaces/{workspace['slug']}/duplicate-assets/{asset_id}/",
        json={"entity_type": "DRAFT_ISSUE_DESCRIPTION"},
    )
    if r.status_code == 200:
        data = r.json()
        assert_has_fields(data, {"asset_id": str}, where="duplicate")
        assert is_uuid(data["asset_id"])
    else:
        assert_status(r, 500)
        assert isinstance(r.json().get("error"), str)
