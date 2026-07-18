"""Contract: workspace analytics endpoints.

Confirmed against the live Python reference server:
- Creating a workspace auto-seeds a demo project full of sample issues, so
  *workspace-wide* totals are volatile. Every test therefore scopes to a project
  it seeds itself via the `?project=<id>` / `?project_ids=<id>` filter, which
  makes the counts deterministic on BOTH servers.
- New issues are created with an EXPLICIT state (the project's seeded "Backlog"
  state). The Python reference assigns a default state automatically; the Go port
  leaves state NULL unless told. Passing `state` explicitly makes the state-group
  grouping deterministic across both servers.

GET /workspaces/<slug>/analytics/         -> {total, distribution, extras{5 keys}}
GET /workspaces/<slug>/default-analytics/ -> summary dict (counts + user rollups)
GET /workspaces/<slug>/project-stats/     -> [ {id, total_issues, ...} ]

Endpoints intentionally NOT covered here (see the module report for reasons):
advance-analytics*, analytic-view CRUD, saved-analytic-view, export-analytics.
"""

import pytest

from lib.shape import assert_status, is_uuid

pytestmark = pytest.mark.analytics


# ---- helpers ---------------------------------------------------------------
def _wa(workspace) -> str:
    return f"/api/workspaces/{workspace['slug']}"


def _states(client, workspace, project):
    r = client.api_get(f"{_wa(workspace)}/projects/{project['id']}/states/")
    assert_status(r, 200)
    body = r.json()
    return body["results"] if isinstance(body, dict) else body


def _backlog_state(client, workspace, project) -> dict:
    for s in _states(client, workspace, project):
        if s["group"] == "backlog":
            return s
    raise AssertionError("no backlog state seeded on project")


# Priority -> number of issues to create in it. All issues go into the Backlog
# state so the state-group rollups are a single deterministic group.
PRIO_COUNTS = {"high": 2, "low": 1, "urgent": 1, "medium": 1, "none": 1}
TOTAL = sum(PRIO_COUNTS.values())  # 6


@pytest.fixture
def seeded(client, workspace, project):
    """A project with TOTAL issues of known priority, all in the Backlog state."""
    backlog = _backlog_state(client, workspace, project)
    for prio, n in PRIO_COUNTS.items():
        for _ in range(n):
            r = client.api_post(
                f"{_wa(workspace)}/projects/{project['id']}/issues/",
                json={"name": "A", "priority": prio, "state": backlog["id"]},
            )
            assert_status(r, 201)
    return {
        "workspace": workspace,
        "project": project,
        "backlog": backlog,
        "pid": project["id"],
    }


# ---- /analytics/ : validation ---------------------------------------------
def test_analytics_requires_x_and_y_axis(client, workspace):
    r = client.api_get(f"{_wa(workspace)}/analytics/")
    assert_status(r, 400)
    assert "error" in r.json()


def test_analytics_rejects_invalid_x_axis(client, workspace):
    r = client.api_get(f"{_wa(workspace)}/analytics/?x_axis=bogus&y_axis=issue_count")
    assert_status(r, 400)
    assert "error" in r.json()


def test_analytics_rejects_invalid_y_axis(client, workspace):
    r = client.api_get(f"{_wa(workspace)}/analytics/?x_axis=priority&y_axis=bogus")
    assert_status(r, 400)
    assert "error" in r.json()


def test_analytics_rejects_segment_equal_to_x_axis(client, workspace):
    r = client.api_get(
        f"{_wa(workspace)}/analytics/?x_axis=priority&y_axis=issue_count&segment=priority"
    )
    assert_status(r, 400)
    assert "error" in r.json()


# ---- /analytics/ : distributions ------------------------------------------
def _analytics(client, seeded, **params):
    q = "&".join(f"{k}={v}" for k, v in params.items())
    r = client.api_get(f"{_wa(seeded['workspace'])}/analytics/?{q}&project={seeded['pid']}")
    assert_status(r, 200)
    return r.json()


EXTRAS_KEYS = {
    "state_details",
    "assignee_details",
    "label_details",
    "cycle_details",
    "module_details",
}


def test_analytics_priority_distribution(client, seeded):
    body = _analytics(client, seeded, x_axis="priority", y_axis="issue_count")
    assert body["total"] == TOTAL
    dist = body["distribution"]
    assert set(dist.keys()) == set(PRIO_COUNTS.keys())
    for prio, n in PRIO_COUNTS.items():
        assert dist[prio] == [{"dimension": prio, "count": n}]
    # extras always present with all five keys
    extras = body["extras"]
    assert set(extras.keys()) == EXTRAS_KEYS
    # priority axis -> no per-key details; the join-table details are empty on
    # this port regardless (no assignees/labels/cycles/modules on the issues).
    for k in EXTRAS_KEYS:
        assert extras[k] in ({}, [])


def test_analytics_state_group_distribution(client, seeded):
    body = _analytics(client, seeded, x_axis="state__group", y_axis="issue_count")
    assert body["total"] == TOTAL
    dist = body["distribution"]
    # every seeded issue is in the Backlog state -> single group
    assert dist == {"backlog": [{"dimension": "backlog", "count": TOTAL}]}
    # state_details only populated for x_axis == state_id, not state__group
    assert body["extras"]["state_details"] in ({}, [])


def test_analytics_state_id_distribution(client, seeded):
    backlog_id = seeded["backlog"]["id"]
    body = _analytics(client, seeded, x_axis="state_id", y_axis="issue_count")
    assert body["total"] == TOTAL
    dist = body["distribution"]
    assert dist == {backlog_id: [{"dimension": backlog_id, "count": TOTAL}]}
    # state_details is a list of {state_id, state__name, state__color}. Colours
    # differ between servers, so pin structure + the id, not the values.
    details = body["extras"]["state_details"]
    assert isinstance(details, list) and len(details) == 1
    row = details[0]
    assert set(row.keys()) == {"state_id", "state__name", "state__color"}
    assert row["state_id"] == backlog_id
    assert isinstance(row["state__name"], str) and row["state__name"]
    assert isinstance(row["state__color"], str)


def test_analytics_priority_segmented_by_state_group(client, seeded):
    body = _analytics(
        client, seeded, x_axis="priority", y_axis="issue_count", segment="state__group"
    )
    assert body["total"] == TOTAL
    dist = body["distribution"]
    assert set(dist.keys()) == set(PRIO_COUNTS.keys())
    for prio, n in PRIO_COUNTS.items():
        assert dist[prio] == [{"dimension": prio, "segment": "backlog", "count": n}]


# ---- /default-analytics/ ---------------------------------------------------
def test_default_analytics(client, seeded):
    r = client.api_get(
        f"{_wa(seeded['workspace'])}/default-analytics/?project={seeded['pid']}"
    )
    assert_status(r, 200)
    body = r.json()

    assert body["total_issues"] == TOTAL
    assert body["total_issues_classified"] == [
        {"state_group": "backlog", "state_count": TOTAL}
    ]
    assert body["open_issues"] == TOTAL
    assert body["open_issues_classified"] == [
        {"state_group": "backlog", "state_count": TOTAL}
    ]
    # nothing completed / assigned on the seeded set
    assert body["issue_completed_month_wise"] == []
    assert body["most_issue_closed_user"] == []
    assert body["open_estimate_sum"] is None
    assert body["total_estimate_sum"] is None

    # created-by rollup: one creator (the client), count == TOTAL.
    created = body["most_issue_created_user"]
    assert isinstance(created, list) and len(created) == 1
    row = created[0]
    assert set(row.keys()) == {
        "created_by__first_name",
        "created_by__last_name",
        "created_by__display_name",
        "created_by__id",
        "count",
        "created_by__avatar_url",
    }
    assert row["created_by__id"] == client.user_id
    assert row["count"] == TOTAL

    # pending (not-completed) rollup: unassigned issues collapse to one null row.
    pending = body["pending_issue_user"]
    assert isinstance(pending, list) and len(pending) == 1
    prow = pending[0]
    assert set(prow.keys()) == {
        "assignees__first_name",
        "assignees__last_name",
        "assignees__display_name",
        "assignees__id",
        "count",
        "assignees__avatar_url",
    }
    assert prow["assignees__id"] is None
    assert prow["count"] == TOTAL


# ---- /project-stats/ -------------------------------------------------------
def test_project_stats_default_fields(client, seeded):
    pid = seeded["pid"]
    r = client.api_get(f"{_wa(seeded['workspace'])}/project-stats/?project_ids={pid}")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list) and len(body) == 1
    row = body[0]
    assert row["id"] == pid
    assert is_uuid(row["id"])
    assert row["total_issues"] == TOTAL
    assert row["completed_issues"] == 0
    assert row["total_cycles"] == 0
    assert row["total_modules"] == 0
    assert row["total_members"] == 1


def test_project_stats_field_selection(client, seeded):
    pid = seeded["pid"]
    r = client.api_get(
        f"{_wa(seeded['workspace'])}/project-stats/?project_ids={pid}&fields=total_issues"
    )
    assert_status(r, 200)
    body = r.json()
    assert len(body) == 1
    row = body[0]
    # only the requested field (+ id) comes back
    assert set(row.keys()) == {"id", "total_issues"}
    assert row["total_issues"] == TOTAL
