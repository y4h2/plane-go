"""Contract: cycle + module archive/unarchive endpoints.

Covers `POST/DELETE .../cycles/<cycle_id>/archive/`, `GET .../archived-cycles/`
(+ `<pk>/` detail), and the same trio for modules.

QUIRKS confirmed live against the Python reference (localhost:8000):

- Archiving a cycle requires `end_date` to already be in the past
  (`end_date >= now()` -> 400 `{"error": "Only completed cycles can be
  archived"}`). A cycle with **no** `end_date` crashes the reference (bare
  `None >= datetime` comparison, unhandled) -> **500**
  `{"error": "Something went wrong please try again later"}`.
- Archiving a module requires `status` to be `"completed"` or `"cancelled"`
  (else 400 `{"error": "Only completed or cancelled modules can be
  archived"}`).
- `POST .../archive/` returns 200 `{"archived_at": <str>}` (not 201), and is
  NOT idempotent-guarded — archiving an already-archived cycle/module just
  re-archives it (200 again, timestamp bumped).
- `DELETE .../cycles/<cycle_id>/archive/` (same URL as POST, method-routed)
  unarchives: 204, no body. It is idempotent — calling it on a cycle/module
  that isn't currently archived still 204s.
- The unarchive URL is keyed on `cycle_id`/`module_id`, NOT on `archived-
  cycles/<pk>/` / `archived-modules/<pk>/` — despite those detail URLs
  existing for GET. DELETE-ing `archived-cycles/<pk>/` (or the modules
  equivalent) hits a view method whose signature doesn't match what that URL
  supplies, so the reference always 500s there regardless of the pk. This
  contradicts a naive reading of the urls.py file; confirmed by probing.
- Archived cycles/modules are excluded from the normal (non-archived)
  `.../cycles/` and `.../modules/` list endpoints, and the normal retrieve
  endpoint 404s for an archived id.
- `GET .../archived-cycles/<pk>/` for a pk that isn't archived (or doesn't
  exist at all) crashes the reference (`NoneType` not subscriptable) -> 500.
- `GET .../archived-modules/<pk>/` for the equivalent case does NOT 500: it
  degrades to a fixed 200 body
  `{"member_ids": [], "estimate_distribution": {}, "distribution":
  {"assignees": [], "labels": [], "completion_chart": {}}}` — a real
  behavioral difference between the cycle and module endpoints, not a typo.
- The normal (non-archived) cycle retrieve shape has NO `archived_at` key at
  all; the normal module retrieve shape DOES (null when not archived). Another
  real cycle/module asymmetry in the reference, not a typo.
"""

import uuid

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_has_fields, assert_status

pytestmark = pytest.mark.archive

CYCLE_ARCHIVE_ITEM_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "description": str,
    "start_date": OPTIONAL(str),
    "end_date": OPTIONAL(str),
    "owned_by_id": str,
    "sort_order": (int, float),
    "archived_at": str,
    "is_favorite": bool,
    "total_issues": int,
    "completed_issues": int,
    "cancelled_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_issues": int,
    "assignee_ids": list,
}

MODULE_ARCHIVE_ITEM_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "description": str,
    "status": str,
    "lead_id": OPTIONAL(str),
    "sort_order": (int, float),
    "archived_at": str,
    "is_favorite": bool,
    "total_issues": int,
    "completed_issues": int,
    "cancelled_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_issues": int,
    "member_ids": list,
}

MODULE_DEGENERATE_DETAIL = {
    "member_ids": [],
    "estimate_distribution": {},
    "distribution": {"assignees": [], "labels": [], "completion_chart": {}},
}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create_cycle(client, workspace, project, **extra) -> dict:
    body = {"name": f"Cy {unique()}", **extra}
    r = client.api_post(_base(workspace, project) + "/cycles/", json=body)
    assert_status(r, 201)
    return r.json()


def _create_past_cycle(client, workspace, project) -> dict:
    """A cycle whose end_date is already in the past -> archivable."""
    return _create_cycle(
        client,
        workspace,
        project,
        start_date="2020-01-01T00:00:00Z",
        end_date="2020-01-10T00:00:00Z",
    )


def _create_module(client, workspace, project, **extra) -> dict:
    body = {"name": f"Mo {unique()}", **extra}
    r = client.api_post(_base(workspace, project) + "/modules/", json=body)
    assert_status(r, 201)
    return r.json()


# ---- cycle archive validation ----------------------------------------------


def test_cycle_archive_requires_completed(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_cycle(
        client,
        workspace,
        project,
        start_date="2020-01-01T00:00:00Z",
        end_date="2099-01-10T00:00:00Z",
    )
    r = client.api_post(base + f"/cycles/{cyc['id']}/archive/")
    assert_status(r, 400)
    assert r.json() == {"error": "Only completed cycles can be archived"}


def test_cycle_archive_without_end_date_errors(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_cycle(client, workspace, project)  # no dates
    r = client.api_post(base + f"/cycles/{cyc['id']}/archive/")
    assert_status(r, 500)


def test_cycle_archive_not_found(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_post(base + f"/cycles/{uuid.uuid4()}/archive/")
    assert_status(r, 404)


def test_cycle_unarchive_not_found(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_delete(base + f"/cycles/{uuid.uuid4()}/archive/")
    assert_status(r, 404)


# ---- cycle archive/unarchive flow ------------------------------------------


def test_cycle_archive_and_unarchive_flow(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_past_cycle(client, workspace, project)
    cid = cyc["id"]

    # Archive.
    r = client.api_post(base + f"/cycles/{cid}/archive/")
    assert_status(r, 200)
    assert_fields(r.json(), {"archived_at": str}, where="cycle-archive")
    assert r.json()["archived_at"]

    # Re-archiving is allowed (not idempotency-guarded) -> 200 again.
    r2 = client.api_post(base + f"/cycles/{cid}/archive/")
    assert_status(r2, 200)

    # Excluded from the normal list; normal retrieve 404s.
    r_list = client.api_get(base + "/cycles/")
    assert_status(r_list, 200)
    assert cid not in [c["id"] for c in r_list.json()]

    r_get = client.api_get(base + f"/cycles/{cid}/")
    assert_status(r_get, 404)

    # Appears in the archived list with the archive shape.
    r_alist = client.api_get(base + "/archived-cycles/")
    assert_status(r_alist, 200)
    mine = [c for c in r_alist.json() if c["id"] == cid]
    assert len(mine) == 1
    assert_has_fields(mine[0], CYCLE_ARCHIVE_ITEM_SHAPE, where="archived-cycle(list)")

    # Archived detail.
    r_adetail = client.api_get(base + f"/archived-cycles/{cid}/")
    assert_status(r_adetail, 200)
    assert_has_fields(r_adetail.json(), CYCLE_ARCHIVE_ITEM_SHAPE, where="archived-cycle(detail)")
    assert r_adetail.json()["id"] == cid

    # Unarchive via the cycle_id-keyed route.
    r_unarchive = client.api_delete(base + f"/cycles/{cid}/archive/")
    assert_status(r_unarchive, 204)

    # Back in the normal list/retrieve. Quirk: the normal cycle retrieve
    # shape has no `archived_at` key at all (unlike modules, see below) —
    # 200 (not 404) is the signal that it's unarchived.
    r_get2 = client.api_get(base + f"/cycles/{cid}/")
    assert_status(r_get2, 200)
    assert "archived_at" not in r_get2.json()


def test_cycle_unarchive_idempotent_when_not_archived(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_cycle(client, workspace, project)  # never archived
    r = client.api_delete(base + f"/cycles/{cyc['id']}/archive/")
    assert_status(r, 204)


def test_cycle_unarchive_via_archived_cycles_pk_errors(client, workspace, project):
    """The `archived-cycles/<pk>/` URL is GET-only in practice: DELETE-ing it
    500s in the reference (kwarg mismatch), unlike `cycles/<cycle_id>/archive/`."""
    base = _base(workspace, project)
    cyc = _create_past_cycle(client, workspace, project)
    r = client.api_post(base + f"/cycles/{cyc['id']}/archive/")
    assert_status(r, 200)
    r2 = client.api_delete(base + f"/archived-cycles/{cyc['id']}/")
    assert_status(r2, 500)


def test_archived_cycles_list_empty_for_fresh_project(client, workspace, project):
    r = client.api_get(_base(workspace, project) + "/archived-cycles/")
    assert_status(r, 200)
    assert r.json() == []


def test_archived_cycle_detail_not_archived_errors(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_cycle(client, workspace, project)  # not archived
    r = client.api_get(base + f"/archived-cycles/{cyc['id']}/")
    assert_status(r, 500)


# ---- module archive validation ---------------------------------------------


def test_module_archive_requires_completed_or_cancelled(client, workspace, project):
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project)  # default status "backlog"
    r = client.api_post(base + f"/modules/{mod['id']}/archive/")
    assert_status(r, 400)
    assert r.json() == {"error": "Only completed or cancelled modules can be archived"}


@pytest.mark.parametrize("status_value", ["completed", "cancelled"])
def test_module_archive_allowed_statuses(client, workspace, project, status_value):
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project, status=status_value)
    r = client.api_post(base + f"/modules/{mod['id']}/archive/")
    assert_status(r, 200)
    assert r.json()["archived_at"]


def test_module_archive_not_found(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_post(base + f"/modules/{uuid.uuid4()}/archive/")
    assert_status(r, 404)


def test_module_unarchive_not_found(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_delete(base + f"/modules/{uuid.uuid4()}/archive/")
    assert_status(r, 404)


# ---- module archive/unarchive flow -----------------------------------------


def test_module_archive_and_unarchive_flow(client, workspace, project):
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project, status="completed")
    mid = mod["id"]

    r = client.api_post(base + f"/modules/{mid}/archive/")
    assert_status(r, 200)
    assert_fields(r.json(), {"archived_at": str}, where="module-archive")
    assert r.json()["archived_at"]

    r_list = client.api_get(base + "/modules/")
    assert_status(r_list, 200)
    assert mid not in [m["id"] for m in r_list.json()]

    r_get = client.api_get(base + f"/modules/{mid}/")
    assert_status(r_get, 404)

    r_alist = client.api_get(base + "/archived-modules/")
    assert_status(r_alist, 200)
    mine = [m for m in r_alist.json() if m["id"] == mid]
    assert len(mine) == 1
    assert_has_fields(mine[0], MODULE_ARCHIVE_ITEM_SHAPE, where="archived-module(list)")

    r_adetail = client.api_get(base + f"/archived-modules/{mid}/")
    assert_status(r_adetail, 200)
    assert_has_fields(r_adetail.json(), MODULE_ARCHIVE_ITEM_SHAPE, where="archived-module(detail)")
    assert r_adetail.json()["id"] == mid

    r_unarchive = client.api_delete(base + f"/modules/{mid}/archive/")
    assert_status(r_unarchive, 204)

    r_get2 = client.api_get(base + f"/modules/{mid}/")
    assert_status(r_get2, 200)
    assert r_get2.json()["archived_at"] is None


def test_module_unarchive_idempotent_when_not_archived(client, workspace, project):
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project)
    r = client.api_delete(base + f"/modules/{mod['id']}/archive/")
    assert_status(r, 204)


def test_module_unarchive_via_archived_modules_pk_errors(client, workspace, project):
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project, status="completed")
    r = client.api_post(base + f"/modules/{mod['id']}/archive/")
    assert_status(r, 200)
    r2 = client.api_delete(base + f"/archived-modules/{mod['id']}/")
    assert_status(r2, 500)


def test_archived_modules_list_empty_for_fresh_project(client, workspace, project):
    r = client.api_get(_base(workspace, project) + "/archived-modules/")
    assert_status(r, 200)
    assert r.json() == []


def test_archived_module_detail_not_archived_degenerate(client, workspace, project):
    """Unlike cycles, a not-archived module's archived-detail does NOT 500 —
    it degrades to a fixed minimal 200 body."""
    base = _base(workspace, project)
    mod = _create_module(client, workspace, project)  # not archived
    r = client.api_get(base + f"/archived-modules/{mod['id']}/")
    assert_status(r, 200)
    assert r.json() == MODULE_DEGENERATE_DETAIL


def test_archived_module_detail_nonexistent_degenerate(client, workspace, project):
    base = _base(workspace, project)
    r = client.api_get(base + f"/archived-modules/{uuid.uuid4()}/")
    assert_status(r, 200)
    assert r.json() == MODULE_DEGENERATE_DETAIL
