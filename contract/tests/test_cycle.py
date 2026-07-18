"""Contract: cycle endpoints.

Covers create/list/retrieve/patch/delete on `B/cycles/`, date-order validation,
`cycles/date-check/`, cycle-issues add + list, and the workspace-wide cycle list.

QUIRKS confirmed live against the Python reference:
- `POST B/cycles/` returns a **bare `.values()` dict** (not an envelope, not the
  nested retrieve serializer) — e.g. it has NO `cancelled_issues` and NO
  `sub_issues`.
- `GET B/cycles/` (project list) items add `cancelled_issues` (vs. create) but
  still no `sub_issues`.
- `GET B/cycles/<id>/` (retrieve) adds `sub_issues` (vs. create/list) but still
  no `cancelled_issues`.
- `PATCH B/cycles/<id>/` echoes the create shape (no `cancelled_issues`, no
  `sub_issues`).
- `GET /api/workspaces/<slug>/cycles/` (workspace-wide) is a DIFFERENT, leaner
  shape: no `created_by`/`is_favorite`/`status`/`version`/`assignee_ids`/
  `owned_by_id`... actually it keeps `owned_by_id` but drops the rest above,
  and adds `started_issues`/`unstarted_issues`/`backlog_issues` on top of
  `cancelled_issues`/`completed_issues`/`total_issues`.
- `start_date`/`end_date` on create/patch accept full ISO datetimes and the
  server normalizes them (start -> +1s, end -> 23:59:00 local) — we only
  assert type/order behavior, never the echoed value.
- Both dates are required together: only one supplied -> 400
  `{"error": "..."}`; start > end -> 400 `{"non_field_errors": [...]}`.
- `POST B/cycles/date-check/` wants **plain `YYYY-MM-DD`** dates (NOT ISO
  datetimes — sending a full ISO datetime 500s, since the view does a bare
  `strptime(date, "%Y-%m-%d")`). Success shape is `{"status": true}` when free,
  or `{"status": false, "error": "..."}` when the range overlaps an existing
  cycle in the project. Missing either date -> 400 `{"error": "..."}`.
- `POST B/cycles/<id>/cycle-issues/` returns **201 `{"message": "success"}`**
  (not the created issues). Empty `issues` list -> 400.
- `GET B/cycles/<id>/cycle-issues/` is the cursor-pagination envelope
  (`assert_envelope`) whose `results` are full ISSUE objects.
- New workspaces come pre-seeded with sample cycles, so list/workspace-wide
  endpoints are never empty — tests only assert on the cycle(s) they created.
"""

import uuid

import pytest

from lib.client import unique
from lib.shape import (
    OPTIONAL,
    assert_envelope,
    assert_has_fields,
    assert_status,
    is_uuid,
)

pytestmark = pytest.mark.cycle

# Shape returned by create / project-list / retrieve / patch. Fields that differ
# per-endpoint (`cancelled_issues`, `sub_issues`) are checked separately with
# assert_has_fields (tolerant of extra keys) rather than folded in here.
CYCLE_BASE_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "description": OPTIONAL(str),
    "start_date": OPTIONAL(str),
    "end_date": OPTIONAL(str),
    "owned_by_id": str,
    "view_props": dict,
    "sort_order": (int, float),
    "external_source": OPTIONAL(str),
    "external_id": OPTIONAL(str),
    "progress_snapshot": dict,
    "logo_props": dict,
    "total_issues": int,
    "completed_issues": int,
    "assignee_ids": list,
}

WORKSPACE_CYCLE_SHAPE = {
    "id": str,
    "workspace_id": str,
    "project_id": str,
    "name": str,
    "start_date": OPTIONAL(str),
    "end_date": OPTIONAL(str),
    "owned_by_id": str,
    "sort_order": (int, float),
    "total_issues": int,
    "cancelled_issues": int,
    "completed_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_issues": int,
}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _create_cycle(client, workspace, project, **extra) -> dict:
    body = {"name": f"Cy {unique()}", **extra}
    r = client.api_post(_base(workspace, project) + "/cycles/", json=body)
    assert_status(r, 201)
    return r.json()


def test_create_cycle(client, workspace, project):
    cyc = _create_cycle(client, workspace, project)
    assert_has_fields(cyc, CYCLE_BASE_SHAPE, where="cycle(create)")
    assert is_uuid(cyc["id"])
    assert cyc["project_id"] == project["id"]
    assert "cancelled_issues" not in cyc
    assert "sub_issues" not in cyc


def test_create_cycle_requires_name(client, workspace, project):
    r = client.api_post(_base(workspace, project) + "/cycles/", json={})
    assert_status(r, 400)


def test_list_cycles(client, workspace, project):
    created = _create_cycle(client, workspace, project)
    r = client.api_get(_base(workspace, project) + "/cycles/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [c for c in body if c["id"] == created["id"]]
    assert len(mine) == 1
    assert_has_fields(mine[0], CYCLE_BASE_SHAPE, where="cycle(list)")
    assert isinstance(mine[0]["cancelled_issues"], int)
    assert "sub_issues" not in mine[0]


def test_retrieve_cycle(client, workspace, project):
    created = _create_cycle(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/cycles/{created['id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, CYCLE_BASE_SHAPE, where="cycle(retrieve)")
    assert isinstance(body["sub_issues"], int)
    assert body["id"] == created["id"]


def test_retrieve_cycle_not_found(client, workspace, project):
    r = client.api_get(_base(workspace, project) + f"/cycles/{uuid.uuid4()}/")
    assert_status(r, 404)


def test_patch_cycle(client, workspace, project):
    created = _create_cycle(client, workspace, project)
    r = client.api_patch(
        _base(workspace, project) + f"/cycles/{created['id']}/",
        json={"name": "Renamed Cycle"},
    )
    assert_status(r, 200)
    body = r.json()
    assert body["name"] == "Renamed Cycle"
    assert_has_fields(body, CYCLE_BASE_SHAPE, where="cycle(patch)")


def test_delete_cycle(client, workspace, project):
    created = _create_cycle(client, workspace, project)
    base = _base(workspace, project)
    r = client.api_delete(base + f"/cycles/{created['id']}/")
    assert_status(r, 204)
    assert client.api_get(base + f"/cycles/{created['id']}/").status_code == 404


def test_create_cycle_date_order_rejected(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/cycles/",
        json={
            "name": f"Cy {unique()}",
            "start_date": "2026-01-10T00:00:00Z",
            "end_date": "2026-01-01T00:00:00Z",
        },
    )
    assert_status(r, 400)


def test_create_cycle_valid_dates_accepted(client, workspace, project):
    r = client.api_post(
        _base(workspace, project) + "/cycles/",
        json={
            "name": f"Cy {unique()}",
            "start_date": "2026-01-01T00:00:00Z",
            "end_date": "2026-01-10T00:00:00Z",
        },
    )
    assert_status(r, 201)
    body = r.json()
    assert body["start_date"] is not None
    assert body["end_date"] is not None


@pytest.mark.parametrize(
    "dates",
    [
        {"start_date": "2026-01-01T00:00:00Z"},
        {"end_date": "2026-01-10T00:00:00Z"},
    ],
    ids=["only_start", "only_end"],
)
def test_create_cycle_one_sided_date_rejected(client, workspace, project, dates):
    r = client.api_post(
        _base(workspace, project) + "/cycles/",
        json={"name": f"Cy {unique()}", **dates},
    )
    assert_status(r, 400)


def test_date_check(client, workspace, project):
    base = _base(workspace, project)
    # A range with no existing cycle -> free.
    r = client.api_post(
        base + "/cycles/date-check/",
        json={"start_date": "2026-05-01", "end_date": "2026-05-10"},
    )
    assert_status(r, 200)
    assert r.json()["status"] is True

    # Create a cycle occupying a range, then check an overlapping range.
    _create_cycle(
        client,
        workspace,
        project,
        start_date="2026-06-01T00:00:00Z",
        end_date="2026-06-10T00:00:00Z",
    )
    r2 = client.api_post(
        base + "/cycles/date-check/",
        json={"start_date": "2026-06-05", "end_date": "2026-06-15"},
    )
    assert_status(r2, 200)
    assert r2.json()["status"] is False


def test_date_check_requires_both_dates(client, workspace, project):
    r = client.api_post(_base(workspace, project) + "/cycles/date-check/", json={})
    assert_status(r, 400)


def test_add_cycle_issues(client, workspace, project):
    base = _base(workspace, project)
    cyc = _create_cycle(client, workspace, project)

    ri = client.api_post(base + "/issues/", json={"name": f"Issue {unique()}"})
    assert_status(ri, 201)
    issue_id = ri.json()["id"]

    r = client.api_post(base + f"/cycles/{cyc['id']}/cycle-issues/", json={"issues": [issue_id]})
    assert_status(r, 201)
    assert r.json() == {"message": "success"}

    rl = client.api_get(base + f"/cycles/{cyc['id']}/cycle-issues/")
    assert_status(rl, 200)
    envelope = rl.json()
    assert_envelope(envelope, where="cycle-issues", grouped=False)
    ids = [i["id"] for i in envelope["results"]]
    assert issue_id in ids


def test_add_cycle_issues_requires_issues(client, workspace, project):
    cyc = _create_cycle(client, workspace, project)
    r = client.api_post(
        _base(workspace, project) + f"/cycles/{cyc['id']}/cycle-issues/", json={"issues": []}
    )
    assert_status(r, 400)


def test_workspace_wide_cycle_list(client, workspace, project):
    created = _create_cycle(client, workspace, project)
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/cycles/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list)
    mine = [c for c in body if c["id"] == created["id"]]
    assert len(mine) == 1
    assert_has_fields(mine[0], WORKSPACE_CYCLE_SHAPE, where="cycle(workspace-list)")
