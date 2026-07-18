"""Contract: issue-peek reads (meta, relations, history, description-versions)
and cycle progress/analytics.

QUIRKS confirmed live against the Python reference (localhost:8000):
- `GET B/issues/<id>/meta/` is a tiny 2-field dict: `sequence_id` (int) +
  `project_identifier` (str, the *project's* identifier, e.g. "PB255563").
  Unknown issue id -> 404 `{"error": "..."}`.
- `GET B/issues/<id>/issue-relation/` is a flat dict of 8 relation-kind keys,
  each a list (empty when the issue has no relations). It does NOT 404 for
  an unknown issue id in the Python reference (returns 200 with all-empty
  lists) — we don't assert on that behavior here, only the shape for a real
  issue.
- `GET B/issues/<id>/history/?activity_type=issue-property` is a bare JSON
  list (not the cursor-pagination envelope). Python returns a non-empty list
  (issue creation is itself a history entry); we only assert it's a list
  since Go may not backfill history yet.
- `GET B/work-items/<id>/description-versions/` uses a DIFFERENT pagination
  envelope than the issue-family cursor envelope (`assert_envelope` in
  lib/shape.py does not apply here) — keys are `cursor`/`page_count`/etc.
  We only assert `results` is a list.
- `POST B/cycles/` with `start_date`/`end_date` set is required to get a
  non-error `analytics` response — a cycle with no dates 400s on
  `.../analytics?type=issues` with `{"error": "Cycle has no start or end
  date"}`.
- `GET B/cycles/<id>/progress/` issue-count fields (`total_issues`,
  `backlog_issues`, `completed_issues`, `cancelled_issues`, `started_issues`,
  `unstarted_issues`) are ints; the paired `*_estimate_points` fields are
  numeric but not consistently int (Python's `total_estimate_points` comes
  back as `0.0`, a float), so those are typed loosely as (int, float).
- `GET B/cycles/<id>/analytics?type=issues` returns
  `{"assignees": [...], "labels": [...], "completion_chart": {...}}` where
  `completion_chart` has one key per day in the cycle's date range (never
  empty for a dated cycle).
"""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status

pytestmark = pytest.mark.peek_progress

ISSUE_META_SHAPE = {
    "sequence_id": int,
    "project_identifier": str,
}

ISSUE_RELATION_SHAPE = {
    "blocking": list,
    "blocked_by": list,
    "duplicate": list,
    "relates_to": list,
    "start_after": list,
    "start_before": list,
    "finish_after": list,
    "finish_before": list,
}

CYCLE_PROGRESS_SHAPE = {
    "total_issues": int,
    "backlog_issues": int,
    "completed_issues": int,
    "cancelled_issues": int,
    "started_issues": int,
    "unstarted_issues": int,
    "backlog_estimate_points": (int, float),
    "unstarted_estimate_points": (int, float),
    "started_estimate_points": (int, float),
    "cancelled_estimate_points": (int, float),
    "completed_estimate_points": (int, float),
    "total_estimate_points": (int, float),
}

CYCLE_ANALYTICS_SHAPE = {
    "assignees": list,
    "labels": list,
    "completion_chart": dict,
}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _issue(client, workspace, project) -> str:
    r = client.api_post(_base(workspace, project) + "/issues/", json={"name": f"Peek {unique()}"})
    assert_status(r, 201)
    return r.json()["id"]


def _cycle(client, workspace, project, *, dated: bool) -> str:
    body = {"name": f"Cy {unique()}"}
    if dated:
        body["start_date"] = "2026-01-01T00:00:00Z"
        body["end_date"] = "2026-01-05T00:00:00Z"
    r = client.api_post(_base(workspace, project) + "/cycles/", json=body)
    assert_status(r, 201)
    return r.json()["id"]


# ---- issue peek -----------------------------------------------------------


def test_issue_meta_shape(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/issues/{iid}/meta/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, ISSUE_META_SHAPE, where="issue-meta")
    assert body["sequence_id"] >= 1
    assert body["project_identifier"] == project["identifier"]


def test_issue_meta_not_found(client, workspace, project):
    import uuid

    r = client.api_get(_base(workspace, project) + f"/issues/{uuid.uuid4()}/meta/")
    assert_status(r, 404)


def test_issue_relation_shape(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/issues/{iid}/issue-relation/")
    assert_status(r, 200)
    assert_has_fields(r.json(), ISSUE_RELATION_SHAPE, where="issue-relation")


def test_issue_history_is_list(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_get(
        _base(workspace, project) + f"/issues/{iid}/history/?activity_type=issue-property"
    )
    assert_status(r, 200)
    assert isinstance(r.json(), list), f"history: want list, got {type(r.json()).__name__}"


def test_issue_description_versions_results_is_list(client, workspace, project):
    iid = _issue(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/work-items/{iid}/description-versions/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict), f"description-versions: want dict envelope, got {type(body).__name__}"
    assert "results" in body, f"description-versions: missing 'results'; present={sorted(body)}"
    assert isinstance(body["results"], list), (
        f"description-versions.results: want list, got {type(body['results']).__name__}"
    )


# ---- cycle progress / analytics -------------------------------------------


def test_cycle_progress_shape(client, workspace, project):
    cid = _cycle(client, workspace, project, dated=True)
    r = client.api_get(_base(workspace, project) + f"/cycles/{cid}/progress/")
    assert_status(r, 200)
    assert_has_fields(r.json(), CYCLE_PROGRESS_SHAPE, where="cycle-progress")


def test_cycle_analytics_issues_shape(client, workspace, project):
    cid = _cycle(client, workspace, project, dated=True)
    r = client.api_get(_base(workspace, project) + f"/cycles/{cid}/analytics?type=issues")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, CYCLE_ANALYTICS_SHAPE, where="cycle-analytics")
    assert len(body["completion_chart"]) > 0, "completion_chart: want one key per day, got empty dict"


def test_cycle_analytics_without_dates_rejected(client, workspace, project):
    cid = _cycle(client, workspace, project, dated=False)
    r = client.api_get(_base(workspace, project) + f"/cycles/{cid}/analytics?type=issues")
    assert_status(r, 400)
