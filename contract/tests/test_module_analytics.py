"""Contract: module progress counts + distribution (the module analog of
cycle's dedicated `progress/`/`analytics/` endpoints).

QUIRK confirmed live against the Python reference (localhost:8000): unlike
cycles, modules have NO separate `progress/`/`analytics/` REST endpoints —
`GET .../modules/<id>/progress/` and `.../analytics/` both 404. Instead this
data is embedded directly in the existing module retrieve/list/create
responses:
  - Flat issue-count fields (the progress analog): `total_issues`,
    `backlog_issues`, `unstarted_issues`, `started_issues`,
    `completed_issues`, `cancelled_issues` — computed by joining the
    module's issues through their current state's `group`.
  - `distribution` (the analytics/burndown analog): `{assignees: [...],
    labels: [...], completion_chart: {...}}`.
    - `assignees`/`labels` group issues by assignee/label with
      total/completed/pending sub-counts. Even when no issue has an
      assignee or label, Python still returns ONE row with all-null
      grouping fields (a LEFT JOIN artifact) rather than an empty list —
      we only assert the list *shape* here, not exact contents, since
      that grouping is inherently data-dependent.
    - `completion_chart` is a burndown: one key per day in
      `[start_date, target_date]`, value = `total_issues -
      cumulative_completed_issues_by(date)`, `null` for any date after
      today. It is `{}` whenever the module has no start_date/target_date
      (or, per `burndown_plot`, no completed module-linked issues would
      matter only once dates are set). We only exercise the
      no-dates -> `{}` case deterministically here; a dated burndown with
      exact day-by-day values is inherently flaky against wall-clock
      "today" in a shared contract suite.
  - `estimate_distribution` mirrors `distribution` but by estimate points;
    it stays `{}` unless the project has point-type estimates configured
    (not exercised here — see test_estimate.py for estimate config).

State groups used below come from the project's seeded default states:
backlog / unstarted / started / completed / cancelled (see test_state.py).
"""

import pytest

from lib.client import unique
from lib.shape import ANY, assert_has_fields, assert_status

pytestmark = pytest.mark.module_analytics

DISTRIBUTION_SHAPE = {"assignees": list, "labels": list, "completion_chart": dict}


def _base(workspace, project) -> str:
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}"


def _states_by_group(client, workspace, project) -> dict:
    r = client.api_get(_base(workspace, project) + "/states/")
    assert_status(r, 200)
    return {s["group"]: s["id"] for s in r.json()}


def _create_module(client, workspace, project, **extra) -> dict:
    r = client.api_post(_base(workspace, project) + "/modules/", json={"name": f"MA {unique()}", **extra})
    assert_status(r, 201)
    return r.json()


def _create_issue_in_group(client, workspace, project, group: str, state_ids: dict) -> str:
    r = client.api_post(
        _base(workspace, project) + "/issues/",
        json={"name": f"MA issue {group} {unique()}", "state_id": state_ids[group]},
    )
    assert_status(r, 201)
    return r.json()["id"]


def _add_issues(client, workspace, project, module_id: str, issue_ids: list) -> None:
    r = client.api_post(
        _base(workspace, project) + f"/modules/{module_id}/issues/", json={"issues": issue_ids}
    )
    assert_status(r, 201)


def test_module_progress_counts_zero_when_empty(client, workspace, project):
    """A freshly created module with no issues has every count at 0."""
    module = _create_module(client, workspace, project)
    r = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/")
    assert_status(r, 200)
    body = r.json()
    for field in (
        "total_issues",
        "backlog_issues",
        "unstarted_issues",
        "started_issues",
        "completed_issues",
        "cancelled_issues",
    ):
        assert body[field] == 0, f"{field}: want 0, got {body[field]!r}"
    assert_has_fields(body["distribution"], DISTRIBUTION_SHAPE, where="module.retrieve.distribution")
    assert body["distribution"]["completion_chart"] == {}
    assert body["estimate_distribution"] == {}


def test_module_progress_counts_by_state_group(client, workspace, project):
    """Deterministic per-group counts: one issue in each of 5 state groups,
    plus a second `backlog` issue to confirm counts aren't just 0/1 flags."""
    states = _states_by_group(client, workspace, project)
    module = _create_module(client, workspace, project)

    issue_ids = [
        _create_issue_in_group(client, workspace, project, "backlog", states),
        _create_issue_in_group(client, workspace, project, "backlog", states),
        _create_issue_in_group(client, workspace, project, "unstarted", states),
        _create_issue_in_group(client, workspace, project, "started", states),
        _create_issue_in_group(client, workspace, project, "completed", states),
        _create_issue_in_group(client, workspace, project, "cancelled", states),
    ]
    _add_issues(client, workspace, project, module["id"], issue_ids)

    r = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/")
    assert_status(r, 200)
    body = r.json()
    assert body["total_issues"] == 6
    assert body["backlog_issues"] == 2
    assert body["unstarted_issues"] == 1
    assert body["started_issues"] == 1
    assert body["completed_issues"] == 1
    assert body["cancelled_issues"] == 1
    assert_has_fields(body["distribution"], DISTRIBUTION_SHAPE, where="module.retrieve.distribution")
    # No start_date/target_date on this module -> burndown stays empty.
    assert body["distribution"]["completion_chart"] == {}


def test_module_progress_counts_reflected_in_list(client, workspace, project):
    """The same computed counts must appear on the list endpoint, not just retrieve."""
    states = _states_by_group(client, workspace, project)
    module = _create_module(client, workspace, project)
    issue_ids = [
        _create_issue_in_group(client, workspace, project, "started", states),
        _create_issue_in_group(client, workspace, project, "completed", states),
    ]
    _add_issues(client, workspace, project, module["id"], issue_ids)

    r = client.api_get(_base(workspace, project) + "/modules/")
    assert_status(r, 200)
    mine = next(m for m in r.json() if m["id"] == module["id"])
    assert mine["total_issues"] == 2
    assert mine["started_issues"] == 1
    assert mine["completed_issues"] == 1
    assert mine["backlog_issues"] == 0


def test_module_progress_counts_ignore_other_modules_issues(client, workspace, project):
    """Counts are per-module: issues in module A must not leak into module B's counts."""
    states = _states_by_group(client, workspace, project)
    module_a = _create_module(client, workspace, project)
    module_b = _create_module(client, workspace, project)

    issue_a = _create_issue_in_group(client, workspace, project, "completed", states)
    issue_b = _create_issue_in_group(client, workspace, project, "started", states)
    _add_issues(client, workspace, project, module_a["id"], [issue_a])
    _add_issues(client, workspace, project, module_b["id"], [issue_b])

    ra = client.api_get(_base(workspace, project) + f"/modules/{module_a['id']}/")
    rb = client.api_get(_base(workspace, project) + f"/modules/{module_b['id']}/")
    assert_status(ra, 200)
    assert_status(rb, 200)
    ba, bb = ra.json(), rb.json()
    assert ba["total_issues"] == 1 and ba["completed_issues"] == 1 and ba["started_issues"] == 0
    assert bb["total_issues"] == 1 and bb["started_issues"] == 1 and bb["completed_issues"] == 0


def test_module_distribution_shape_with_issues(client, workspace, project):
    """With real issues linked, `distribution.assignees`/`labels` stay list-shaped
    (Python emits one row per group, including an all-null row when no issue has
    an assignee/label — we only pin the type here, not row contents)."""
    states = _states_by_group(client, workspace, project)
    module = _create_module(client, workspace, project)
    issue_ids = [
        _create_issue_in_group(client, workspace, project, "backlog", states),
        _create_issue_in_group(client, workspace, project, "completed", states),
    ]
    _add_issues(client, workspace, project, module["id"], issue_ids)

    r = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/")
    assert_status(r, 200)
    dist = r.json()["distribution"]
    assert_has_fields(dist, DISTRIBUTION_SHAPE, where="module.retrieve.distribution")
    for row in dist["assignees"]:
        assert_has_fields(
            row,
            {
                "assignee_id": ANY,
                "total_issues": int,
                "completed_issues": int,
                "pending_issues": int,
            },
            where="module.retrieve.distribution.assignees[0]",
        )
    for row in dist["labels"]:
        assert_has_fields(
            row,
            {"label_id": ANY, "total_issues": int, "completed_issues": int, "pending_issues": int},
            where="module.retrieve.distribution.labels[0]",
        )


def test_module_no_dedicated_progress_or_analytics_endpoint(client, workspace, project):
    """Unlike cycles, modules have no standalone progress/analytics route —
    the data lives on the module detail response instead (see module docstring)."""
    module = _create_module(client, workspace, project)
    for suffix in ("progress", "analytics"):
        r = client.api_get(_base(workspace, project) + f"/modules/{module['id']}/{suffix}/")
        assert_status(r, 404)
