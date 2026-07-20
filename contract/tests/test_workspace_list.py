"""Contract: workspace-level list gaps.

Covers `GET /api/workspaces/` and `GET /api/workspaces/{slug}/project-members/`.

`GET /api/workspaces/` is a genuine bug in the Django reference: `WorkSpaceViewSet.list`
is decorated with `@allow_permission([...], level="WORKSPACE")`, which unconditionally
does `kwargs["slug"]` to check the caller's role. The `list` route (`/workspaces/`, no
`{slug}` in the URL) never populates that kwarg, so every authenticated call raises a
bare `KeyError` that `BaseViewSet.handle_exception` maps to a fixed 400 envelope. This
happens for *every* authenticated caller regardless of workspace membership -- it is
not a permissions check, just a crash converted to a generic error. The Go port must
reproduce this exact status + body so frontend code paths (and other contract tests)
that hit this endpoint see identical behavior.
"""

import pytest

from lib.shape import assert_status

pytestmark = pytest.mark.workspace_list


def test_list_workspaces_endpoint_is_broken_by_design(client):
    """GET /api/workspaces/ always 400s for authenticated users (KeyError on
    kwargs["slug"] inside the WORKSPACE-level allow_permission decorator, which the
    `list` action never populates). Confirmed stable across repeated calls and
    independent of whether the caller owns any workspaces."""
    r1 = client.api_get("/api/workspaces/")
    assert_status(r1, 400)
    assert r1.json() == {"error": "The required key does not exist."}

    # Deterministic, not a one-off: same body on a second call.
    r2 = client.api_get("/api/workspaces/")
    assert_status(r2, 400)
    assert r2.json() == {"error": "The required key does not exist."}


def test_list_workspaces_trailing_slash_variants(client):
    # No trailing slash still resolves to the same view (Django APPEND_SLASH).
    r = client.api_get("/api/workspaces")
    assert_status(r, 400)
    assert r.json() == {"error": "The required key does not exist."}


def test_project_members_groups_by_project(client, workspace):
    project = client.create_project(workspace["slug"])
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/project-members/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict)
    assert project["id"] in body, f"expected project {project['id']} in keys {list(body)}"

    entries = body[project["id"]]
    assert isinstance(entries, list) and len(entries) >= 1
    mine = [e for e in entries if e["member"] == client.user_id]
    assert len(mine) == 1, f"expected exactly one entry for {client.user_id} in {entries}"
    entry = mine[0]
    assert set(entry.keys()) == {"id", "role", "member", "original_role", "created_at"}
    assert entry["role"] == 20  # creator is admin
    assert entry["original_role"] == 20
    from lib.shape import is_uuid

    assert is_uuid(entry["id"])
    assert isinstance(entry["created_at"], str)


def test_project_members_unknown_workspace_404s(client):
    r = client.api_get("/api/workspaces/does-not-exist-zzz/project-members/")
    assert r.status_code in (403, 404)
