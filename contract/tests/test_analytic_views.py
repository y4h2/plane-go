"""Contract: saved analytics views (apps/api/plane/app/urls/analytic.py ->
AnalyticViewViewset `analytic-view` CRUD + SavedAnalyticEndpoint
`saved-analytic-view` read).

Quirks discovered by probing the Python reference directly:

- AnalyticViewViewset uses DRF `permission_classes = [WorkSpaceAdminPermission]`
  (not the `allow_permission` decorator most other views use). Despite the
  name, WorkSpaceAdminPermission actually allows role in {ADMIN, MEMBER}, not
  admins only. A bad/missing workspace slug -> DRF's generic 403,
  `{"detail": "You do not have permission to perform this action."}`.
- SavedAnalyticEndpoint instead uses
  `@allow_permission([ROLE.ADMIN, ROLE.MEMBER], level="WORKSPACE")`, whose
  403 body is the differently-shaped `{"error": "You don't have the required
  permissions."}`. Two endpoints, same file, genuinely different 403 shapes.
- retrieve/update/destroy 404s go through ModelViewSet's default
  `get_object()` -> `{"detail": "No AnalyticView matches the given query."}`.
  SavedAnalyticEndpoint's manual `.get()` 404s instead render as
  `{"error": "The required object does not exist."}`.
- name/description are trimmed (DRF CharField default `trim_whitespace=True`)
  both for validation (a whitespace-only name is rejected as blank) and for
  the stored value.
- AnalyticViewSerializer.update has a real typo bug (reads
  `validated_data["query_data"]`, not `"query_dict"`): every PATCH -- no
  matter the payload -- resets the stored `query` back to `{}`, even though
  `query_dict` itself saves normally. "workspace" and "query" are read-only:
  values sent for them are silently ignored.
- On create, `query` is derived from `query_dict` via
  `issue_filters(query_dict, "POST")`, whose non-GET branch assigns raw
  scalar values straight to Django `__in` lookups instead of parsing them
  into lists -- e.g. `query_dict={"priority": "high"}` becomes
  `query={"priority__in": "high"}` (a bare string, not `["high"]`).
- SavedAnalyticEndpoint's own queryset,
  `Issue.issue_objects.filter(**analytic_view.query)`, has NO workspace
  scoping at all -- `query` never carries a workspace key. With an *empty*
  query (created with no extra query_dict filter keys) this endpoint
  aggregates over every non-draft, non-deleted issue instance-wide, not just
  the caller's workspace. And because of the `__in`-on-a-raw-string bug
  above, any *non-empty* `query` -- for any ordinary multi-character filter
  value -- can only ever match a single-character field value, so in
  practice it always yields `{"total": 0, "distribution": {}}`.
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.analytic_views

VIEW_FIELDS = {
    "id": str,
    "created_at": str,
    "updated_at": str,
    "deleted_at": OPTIONAL(str),
    "name": str,
    "description": str,
    "query": dict,
    "query_dict": dict,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "workspace": str,
}


def _base(workspace) -> str:
    return f"/api/workspaces/{workspace['slug']}/analytic-view/"


def _create(client, workspace, **extra):
    payload = {"name": "AV " + unique(), **extra}
    r = client.api_post(_base(workspace), json=payload)
    return r


# ---- AnalyticViewViewset: create -------------------------------------------


def test_create_minimal(client, workspace):
    r = _create(client, workspace)
    assert_status(r, 201)
    v = r.json()
    assert_has_fields(v, VIEW_FIELDS, where="analytic_view")
    assert is_uuid(v["id"])
    assert v["description"] == ""
    assert v["query"] == {}
    assert v["query_dict"] == {}
    assert v["workspace"] == workspace["id"]
    assert is_uuid(v["created_by"])
    assert v["updated_by"] is None


def test_create_with_description_and_query_dict(client, workspace):
    r = _create(client, workspace, description="  desc here  ", query_dict={"priority": "high"})
    assert_status(r, 201)
    v = r.json()
    assert v["description"] == "desc here"  # trimmed
    assert v["query_dict"] == {"priority": "high"}
    # Quirk: issue_filters(..., "POST") assigns the raw string, not a list.
    assert v["query"] == {"priority__in": "high"}


def test_create_name_trimmed(client, workspace):
    raw_name = "  Padded " + unique() + "  "
    r = _create(client, workspace, name=raw_name)
    assert_status(r, 201)
    assert r.json()["name"] == raw_name.strip()


def test_create_missing_name(client, workspace):
    r = client.api_post(_base(workspace), json={"description": "no name"})
    assert_status(r, 400)
    assert r.json() == {"name": ["This field is required."]}


def test_create_blank_name(client, workspace):
    r = client.api_post(_base(workspace), json={"name": ""})
    assert_status(r, 400)
    assert r.json() == {"name": ["This field may not be blank."]}


def test_create_whitespace_only_name_is_blank(client, workspace):
    r = client.api_post(_base(workspace), json={"name": "   "})
    assert_status(r, 400)
    assert r.json() == {"name": ["This field may not be blank."]}


def test_create_name_too_long(client, workspace):
    r = client.api_post(_base(workspace), json={"name": "x" * 300})
    assert_status(r, 400)
    assert r.json() == {"name": ["Ensure this field has no more than 255 characters."]}


# ---- list / retrieve --------------------------------------------------------


def test_list(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    rl = client.api_get(_base(workspace))
    assert_status(rl, 200)
    items = rl.json()
    assert isinstance(items, list)
    match = next((v for v in items if v["id"] == vid), None)
    assert match is not None
    assert_has_fields(match, VIEW_FIELDS, where="analytic_view.list_item")


def test_retrieve(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    rr = client.api_get(_base(workspace) + f"{vid}/")
    assert_status(rr, 200)
    assert rr.json()["id"] == vid


def test_retrieve_nonexistent(client, workspace):
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_get(_base(workspace) + f"{fake}/")
    assert_status(r, 404)
    assert r.json() == {"detail": "No AnalyticView matches the given query."}


# ---- update ------------------------------------------------------------------


def test_update_name(client, workspace):
    r = _create(client, workspace, query_dict={"priority": "high"})
    vid = r.json()["id"]
    assert r.json()["query"] == {"priority__in": "high"}

    ru = client.api_patch(_base(workspace) + f"{vid}/", json={"name": "Updated Name"})
    assert_status(ru, 200)
    v = ru.json()
    assert v["name"] == "Updated Name"
    # Quirk: ANY patch resets `query` to {} (serializer typo bug), even
    # though this request didn't touch query_dict at all.
    assert v["query"] == {}
    assert v["query_dict"] == {"priority": "high"}  # unaffected
    assert is_uuid(v["updated_by"])


def test_update_query_dict_does_not_repopulate_query(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    ru = client.api_patch(_base(workspace) + f"{vid}/", json={"query_dict": {"priority": "urgent"}})
    assert_status(ru, 200)
    v = ru.json()
    assert v["query_dict"] == {"priority": "urgent"}
    assert v["query"] == {}  # still reset, not derived from the new query_dict


def test_update_readonly_fields_ignored(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    ru = client.api_patch(
        _base(workspace) + f"{vid}/",
        json={"workspace": "00000000-0000-0000-0000-000000000000", "query": {"x": 1}},
    )
    assert_status(ru, 200)
    v = ru.json()
    assert v["workspace"] == workspace["id"]
    assert v["query"] == {}


def test_update_empty_body(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    orig = r.json()
    ru = client.api_patch(_base(workspace) + f"{vid}/", json={})
    assert_status(ru, 200)
    v = ru.json()
    assert v["name"] == orig["name"]
    assert is_uuid(v["updated_by"])  # updated_by set even with no real change


def test_update_blank_name_rejected(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    ru = client.api_patch(_base(workspace) + f"{vid}/", json={"name": ""})
    assert_status(ru, 400)
    assert ru.json() == {"name": ["This field may not be blank."]}


def test_update_nonexistent(client, workspace):
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_patch(_base(workspace) + f"{fake}/", json={"name": "x"})
    assert_status(r, 404)
    assert r.json() == {"detail": "No AnalyticView matches the given query."}


# ---- delete ------------------------------------------------------------------


def test_delete(client, workspace):
    r = _create(client, workspace)
    vid = r.json()["id"]
    rd = client.api_delete(_base(workspace) + f"{vid}/")
    assert_status(rd, 204)
    rg = client.api_get(_base(workspace) + f"{vid}/")
    assert_status(rg, 404)


def test_delete_nonexistent(client, workspace):
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_delete(_base(workspace) + f"{fake}/")
    assert_status(r, 404)
    assert r.json() == {"detail": "No AnalyticView matches the given query."}


# ---- permissions / auth -------------------------------------------------------


def test_invalid_workspace_slug(client):
    r = client.api_get("/api/workspaces/does-not-exist-workspace-slug/analytic-view/")
    assert_status(r, 403)
    assert r.json() == {"detail": "You do not have permission to perform this action."}


def test_create_invalid_workspace_slug(client):
    r = client.api_post(
        "/api/workspaces/does-not-exist-workspace-slug/analytic-view/", json={"name": "x"}
    )
    assert_status(r, 403)
    assert r.json() == {"detail": "You do not have permission to perform this action."}


def test_unauthenticated_rejected(base_url):
    import requests

    r = requests.get(f"{base_url}/api/workspaces/anything/analytic-view/", timeout=30)
    assert_status(r, 401)
    assert r.json() == {"detail": "Authentication credentials were not provided."}


# ---- SavedAnalyticEndpoint -----------------------------------------------------


def _saved_base(workspace, vid) -> str:
    return f"/api/workspaces/{workspace['slug']}/saved-analytic-view/{vid}/"


def test_saved_view_missing_axis(client, workspace):
    r = _create(client, workspace)  # no x_axis/y_axis in query_dict
    vid = r.json()["id"]
    rs = client.api_get(_saved_base(workspace, vid))
    assert_status(rs, 400)
    assert rs.json() == {
        "error": "x-axis and y-axis dimensions are required and the values should be valid"
    }


def test_saved_view_segment_equals_x_axis(client, workspace):
    r = _create(client, workspace, query_dict={"x_axis": "priority", "y_axis": "issue_count"})
    vid = r.json()["id"]
    rs = client.api_get(_saved_base(workspace, vid), params={"segment": "priority"})
    assert_status(rs, 400)
    assert rs.json() == {
        "error": "Both segment and x axis cannot be same and segment should be valid"
    }


def test_saved_view_no_filter_aggregates_instance_wide(client, workspace, project):
    """Quirk: with an empty stored `query`, SavedAnalyticEndpoint's queryset
    carries no workspace scoping at all, so creating a matching issue in
    *this* workspace/project still moves the (instance-wide) total."""
    r = _create(client, workspace, query_dict={"x_axis": "priority", "y_axis": "issue_count"})
    vid = r.json()["id"]

    before = client.api_get(_saved_base(workspace, vid))
    assert_status(before, 200)
    total_before = before.json()["total"]

    ri = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/issues/",
        json={"name": "Saved view probe " + unique(), "priority": "urgent"},
    )
    assert_status(ri, 201)

    after = client.api_get(_saved_base(workspace, vid))
    assert_status(after, 200)
    body = after.json()
    assert_has_fields(body, {"total": int, "distribution": dict}, where="saved_view")
    assert body["total"] >= total_before + 1
    urgent_bucket = body["distribution"].get("urgent")
    assert urgent_bucket is not None
    assert any(item["dimension"] == "urgent" and item["count"] >= 1 for item in urgent_bucket)


def test_saved_view_with_filter_always_empty(client, workspace, project):
    """Quirk: a non-empty query_dict filter key produces a `query` whose
    Django `__in` lookup is handed a raw (unsplit) string -- which, for any
    realistic multi-character value, can never match a real field value. The
    saved view therefore always reports zero, even though a matching issue
    exists."""
    ri = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/issues/",
        json={"name": "Filtered probe " + unique(), "priority": "high"},
    )
    assert_status(ri, 201)

    r = _create(
        client,
        workspace,
        query_dict={"x_axis": "priority", "y_axis": "issue_count", "priority": "high"},
    )
    vid = r.json()["id"]
    assert r.json()["query"] == {"priority__in": "high"}

    rs = client.api_get(_saved_base(workspace, vid))
    assert_status(rs, 200)
    assert rs.json() == {"total": 0, "distribution": {}}


def test_saved_view_nonexistent(client, workspace):
    fake = "00000000-0000-0000-0000-000000000000"
    rs = client.api_get(_saved_base(workspace, fake))
    assert_status(rs, 404)
    assert rs.json() == {"error": "The required object does not exist."}


def test_saved_view_invalid_workspace_slug(client):
    fake = "00000000-0000-0000-0000-000000000000"
    r = client.api_get(f"/api/workspaces/does-not-exist-workspace-slug/saved-analytic-view/{fake}/")
    assert_status(r, 403)
    assert r.json() == {"error": "You don't have the required permissions."}


def test_saved_view_unauthenticated_rejected(base_url):
    import requests

    fake = "00000000-0000-0000-0000-000000000000"
    r = requests.get(f"{base_url}/api/workspaces/anything/saved-analytic-view/{fake}/", timeout=30)
    assert_status(r, 401)
