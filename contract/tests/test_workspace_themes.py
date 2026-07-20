"""Contract: workspace themes (per-workspace saved UI theme presets).

Django: apps/api/plane/app/views/workspace/base.py -> WorkspaceThemeViewSet
(urls/workspace.py: workspace-themes). `permission_classes =
[WorkSpaceAdminPermission]` -- despite the name this allows role in
{ADMIN=20, MEMBER=15}, not just admins (see other *_extras/analytic_views
contract suites for the same quirk). A missing/insufficient-role workspace ->
DRF's generic `{"detail": "You do not have permission to perform this
action."}` 403, never a 404.

Quirks pinned by probing the Python reference directly (not obvious from
reading the view/serializer alone):

  - WorkspaceThemeSerializer uses `fields = "__all__"` with
    `read_only_fields = ["workspace", "actor"]`. The model's `unique_together
    = ["workspace", "name", "deleted_at"]` constraint makes DRF's
    UniqueTogetherValidator require every field in that tuple to be supplied
    by the client unless it is `read_only` -- and `deleted_at` (unlike
    `workspace`/`actor`) is NOT read-only. So a POST that omits `deleted_at`
    entirely always 400s with `{"deleted_at": ["This field is required."]}`,
    even though every other field is valid. The only way to create a theme
    is to explicitly send `"deleted_at": null` in the payload. This reads
    like an upstream bug, but it is the real, live contract -- replicated
    verbatim, not "fixed".
  - Because `deleted_at` is writable, PATCH can set it directly too: sending
    a real timestamp soft-deletes the row (it drops out of every subsequent
    GET/LIST, whose querysets all filter `deleted_at is null`), while POST
    can create a theme that is *born* soft-deleted the same way.
  - `colors` is a plain `JSONField(default=dict)` with no shape validation:
    any JSON-serializable value (list, string, ...) is accepted and stored
    verbatim, not just objects.
  - `name` is a required, non-blank, max_length=300 CharField with the DRF
    default `trim_whitespace=True` (whitespace-only counts as blank).
  - `unique_together` on (workspace, name) while not soft-deleted: a
    duplicate name in the same workspace raises IntegrityError, caught by
    BaseViewSet.handle_exception and rendered as
    `{"error": "The payload is not valid"}` (400) -- not a per-field error.
  - PATCH always touches the row (bumps `updated_at`/sets `updated_by`)
    even with an empty JSON body, since ModelViewSet.partial_update always
    calls `serializer.save()`.
  - list() has no per-user scoping: it returns every non-deleted theme in
    the workspace regardless of which member created it (`get_queryset`
    only filters `workspace__slug`).
  - An invalid (non-UUID) `pk` path segment never reaches the view at all --
    Django's own URL routing 404s first, rendered as
    `{"error": "Page not found."}` (the app-wide 404 handler's envelope),
    distinct from DRF's `{"detail": "No WorkspaceTheme matches the given
    query."}` for a well-formed but nonexistent id.
"""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.workspace_themes


def _base(workspace):
    return f"/api/workspaces/{workspace['slug']}/workspace-themes/"


def _create(client, workspace, name=None, colors=None, deleted_at=None):
    body = {"name": name or ("Theme " + unique()), "deleted_at": deleted_at}
    if colors is not None:
        body["colors"] = colors
    return client.api_post(_base(workspace), json=body)


THEME_SPEC = {
    "id": str,
    "created_at": str,
    "updated_at": str,
    "deleted_at": (str, type(None)),
    "name": str,
    "colors": (dict, list, str, int, float, bool, type(None)),
    "created_by": (str, type(None)),
    "updated_by": (str, type(None)),
    "workspace": str,
    "actor": str,
}


# ---- create -----------------------------------------------------------------


def test_create_without_deleted_at_fails(client, workspace):
    """The signature quirk: omitting `deleted_at` always 400s."""
    r = client.api_post(_base(workspace), json={"name": "NoDeletedAt" + unique()})
    assert_status(r, 400)
    assert "deleted_at" in r.json()


def test_create_with_deleted_at_null_succeeds(client, workspace):
    r = _create(client, workspace, colors={"primary": "#000000", "background": "#ffffff"})
    assert_status(r, 201)
    theme = r.json()
    assert_has_fields(theme, THEME_SPEC, where="theme")
    assert is_uuid(theme["id"])
    assert theme["deleted_at"] is None
    assert theme["workspace"] == workspace["id"]
    assert theme["colors"] == {"primary": "#000000", "background": "#ffffff"}


def test_create_defaults_colors_to_empty_dict(client, workspace):
    r = _create(client, workspace)
    assert_status(r, 201)
    assert r.json()["colors"] == {}


def test_create_colors_accepts_non_dict_json(client, workspace):
    r = _create(client, workspace, colors=[1, 2, 3])
    assert_status(r, 201)
    assert r.json()["colors"] == [1, 2, 3]

    r = _create(client, workspace, colors="just-a-string")
    assert_status(r, 201)
    assert r.json()["colors"] == "just-a-string"


def test_create_missing_name(client, workspace):
    r = client.api_post(_base(workspace), json={"deleted_at": None})
    assert_status(r, 400)
    assert "name" in r.json()


def test_create_blank_name(client, workspace):
    r = _create(client, workspace, name="    ")
    assert_status(r, 400)
    assert "name" in r.json()


def test_create_name_too_long(client, workspace):
    r = _create(client, workspace, name="x" * 301)
    assert_status(r, 400)
    assert "name" in r.json()


def test_create_duplicate_name(client, workspace):
    name = "Dup " + unique()
    r1 = _create(client, workspace, name=name)
    assert_status(r1, 201)
    r2 = _create(client, workspace, name=name)
    assert_status(r2, 400)
    assert r2.json() == {"error": "The payload is not valid"}


def test_create_deleted_at_bad_format(client, workspace):
    r = _create(client, workspace, deleted_at="not-a-date")
    assert_status(r, 400)
    assert "deleted_at" in r.json()


def test_create_born_soft_deleted(client, workspace):
    """POST accepts a real `deleted_at` timestamp too -- the created row is
    immediately invisible to GET/LIST."""
    r = _create(client, workspace, deleted_at="2020-01-01T00:00:00Z")
    assert_status(r, 201)
    tid = r.json()["id"]
    rr = client.api_get(_base(workspace) + f"{tid}/")
    assert_status(rr, 404)


# ---- list ---------------------------------------------------------------


def test_list_empty(client, workspace):
    r = client.api_get(_base(workspace))
    assert_status(r, 200)
    assert r.json() == []


def test_list_after_create(client, workspace):
    r = _create(client, workspace)
    tid = r.json()["id"]
    rl = client.api_get(_base(workspace))
    assert_status(rl, 200)
    body = rl.json()
    assert isinstance(body, list)
    assert any(t["id"] == tid for t in body)
    for t in body:
        assert_has_fields(t, THEME_SPEC, where="theme")


# ---- retrieve -------------------------------------------------------------


def test_retrieve(client, workspace):
    r = _create(client, workspace, colors={"a": 1})
    tid = r.json()["id"]
    rr = client.api_get(_base(workspace) + f"{tid}/")
    assert_status(rr, 200)
    assert rr.json()["id"] == tid
    assert rr.json()["colors"] == {"a": 1}


def test_retrieve_nonexistent(client, workspace):
    import uuid

    rr = client.api_get(_base(workspace) + f"{uuid.uuid4()}/")
    assert_status(rr, 404)
    assert rr.json() == {"detail": "No WorkspaceTheme matches the given query."}


def test_retrieve_invalid_uuid(client, workspace):
    rr = client.api_get(_base(workspace) + "not-a-uuid/")
    assert_status(rr, 404)
    assert rr.json() == {"error": "Page not found."}


# ---- patch ------------------------------------------------------------------


def test_patch_updates_name_and_colors(client, workspace):
    r = _create(client, workspace, name="Before" + unique(), colors={"x": 1})
    tid = r.json()["id"]
    rp = client.api_patch(
        _base(workspace) + f"{tid}/", json={"name": "After" + unique(), "colors": {"x": 2}}
    )
    assert_status(rp, 200)
    body = rp.json()
    assert body["colors"] == {"x": 2}
    assert body["updated_by"] is not None


def test_patch_empty_body_still_touches_row(client, workspace):
    r = _create(client, workspace)
    theme = r.json()
    tid = theme["id"]
    assert theme["updated_by"] is None
    rp = client.api_patch(_base(workspace) + f"{tid}/", json={})
    assert_status(rp, 200)
    body = rp.json()
    assert body["name"] == theme["name"]
    assert body["colors"] == theme["colors"]
    # ModelViewSet.partial_update always calls serializer.save(), so
    # updated_by flips from null to the caller even with no field changes.
    assert body["updated_by"] is not None


def test_patch_workspace_and_actor_are_read_only(client, workspace):
    import uuid

    r = _create(client, workspace)
    theme = r.json()
    tid = theme["id"]
    rp = client.api_patch(
        _base(workspace) + f"{tid}/",
        json={"workspace": str(uuid.uuid4()), "actor": str(uuid.uuid4())},
    )
    assert_status(rp, 200)
    body = rp.json()
    assert body["workspace"] == theme["workspace"]
    assert body["actor"] == theme["actor"]


def test_patch_deleted_at_soft_deletes(client, workspace):
    """The same read/write quirk as create: PATCH can set `deleted_at`
    directly since it isn't read-only, immediately soft-deleting the row."""
    r = _create(client, workspace)
    tid = r.json()["id"]
    rp = client.api_patch(_base(workspace) + f"{tid}/", json={"deleted_at": "2020-01-01T00:00:00Z"})
    assert_status(rp, 200)
    assert rp.json()["deleted_at"] is not None

    rr = client.api_get(_base(workspace) + f"{tid}/")
    assert_status(rr, 404)


def test_patch_nonexistent(client, workspace):
    import uuid

    rp = client.api_patch(_base(workspace) + f"{uuid.uuid4()}/", json={"name": "x"})
    assert_status(rp, 404)
    assert rp.json() == {"detail": "No WorkspaceTheme matches the given query."}


# ---- delete -----------------------------------------------------------------


def test_delete(client, workspace):
    r = _create(client, workspace)
    tid = r.json()["id"]
    rd = client.api_delete(_base(workspace) + f"{tid}/")
    assert_status(rd, 204)

    rr = client.api_get(_base(workspace) + f"{tid}/")
    assert_status(rr, 404)


def test_delete_already_deleted(client, workspace):
    r = _create(client, workspace)
    tid = r.json()["id"]
    assert_status(client.api_delete(_base(workspace) + f"{tid}/"), 204)
    rd2 = client.api_delete(_base(workspace) + f"{tid}/")
    assert_status(rd2, 404)


# ---- auth / permissions ------------------------------------------------------


def test_unauthenticated_401(base_url, workspace):
    import requests

    r = requests.get(f"{base_url}{_base(workspace)}")
    assert_status(r, 401)

    r = requests.post(f"{base_url}{_base(workspace)}", json={"name": "x", "deleted_at": None})
    assert_status(r, 401)


def test_non_member_forbidden(workspace, fresh_client):
    r = fresh_client.api_get(_base(workspace))
    assert_status(r, 403)

    r = fresh_client.api_post(_base(workspace), json={"name": "x", "deleted_at": None})
    assert_status(r, 403)


def test_bad_workspace_slug_forbidden(client):
    r = client.api_get("/api/workspaces/does-not-exist-ws-slug/workspace-themes/")
    assert_status(r, 403)
