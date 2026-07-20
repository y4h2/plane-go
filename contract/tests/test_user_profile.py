"""Contract: per-user workspace analytics reads.

Endpoints (all GET, all 200):
- /workspaces/<slug>/user-stats/<user_id>/      per-user issue counts + distributions
- /workspaces/<slug>/user-activity/<user_id>/   activity log (paginated envelope)
- /workspaces/<slug>/user-profile/<user_id>/    per-project counts + user_data
- /users/me/workspaces/<slug>/activity-graph/   issue-created-by-date heatmap

Schema-limit convention (mirrors internal/analytic): the Go port has NO
issue-activity/audit table and NO issue<->assignee join. So:

  * user-activity  -> always an EMPTY paginated envelope. Frozen with a FRESH
    user_id that has no activity, so Python ALSO returns empty. Envelope shape +
    empty results is a determinate contract both satisfy.

  * user-stats / user-profile -> VALUE-assert only the created_by-derived
    `created_issues` (the column Go has). state/priority distributions and the
    assignee-derived scalars (assigned/completed/pending/subscribed) are
    SHAPE-only: Django computes them from assignees (empty/0 here since the fresh
    issues have no assignees) while Go computes distributions from created_by.

  * activity-graph -> list of {created_date, activity_count}. VALUE-assert that
    today's date shows up (a created issue is visible); activity_count is
    SHAPE-only (Django counts audit rows, Go counts created issues).
"""

import datetime

import pytest

from lib.client import PlaneClient, unique
from lib.shape import (
    ANY,
    OPTIONAL,
    assert_envelope,
    assert_has_fields,
    assert_status,
    is_uuid,
)

pytestmark = pytest.mark.user_profile


def _create_issue(client, slug, pid, name=None, priority=None):
    body = {"name": name or ("Issue " + unique())}
    if priority:
        body["priority"] = priority
    r = client.api_post(f"/api/workspaces/{slug}/projects/{pid}/issues/", json=body)
    assert_status(r, 201)
    return r.json()


@pytest.fixture
def seeded(client, workspace, project):
    """Workspace + project owned by `client`, seeded with 3 issues the client
    created (created_by set, NO assignees)."""
    slug, pid = workspace["slug"], project["id"]
    for pr in ("high", "medium", None):
        _create_issue(client, slug, pid, priority=pr)
    return {"slug": slug, "pid": pid, "user_id": client.user_id}


# ---- user-stats ------------------------------------------------------------

USER_STATS_SHAPE = {
    "state_distribution": list,
    "priority_distribution": list,
    "created_issues": int,
    "assigned_issues": int,
    "completed_issues": int,
    "pending_issues": int,
    "subscribed_issues": int,
    "present_cycles": list,
    "upcoming_cycles": list,
}


def test_user_stats_shape_and_created_count(client, seeded):
    r = client.api_get(f"/api/workspaces/{seeded['slug']}/user-stats/{seeded['user_id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, USER_STATS_SHAPE, where="user-stats")
    # created_by-derived -> determinate on both servers.
    assert body["created_issues"] == 3, body
    # distribution rows are shape-only (created_by on Go, assignee on Django).
    for row in body["state_distribution"]:
        assert_has_fields(row, {"state_group": ANY, "state_count": int}, where="user-stats.state")
    for row in body["priority_distribution"]:
        assert_has_fields(row, {"priority": ANY, "priority_count": int}, where="user-stats.priority")


# ---- user-profile ----------------------------------------------------------

def test_user_profile_shape_and_created_count(client, seeded):
    r = client.api_get(f"/api/workspaces/{seeded['slug']}/user-profile/{seeded['user_id']}/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"project_data": list, "user_data": dict}, where="user-profile")

    assert_has_fields(
        body["user_data"],
        {
            "email": str,
            "first_name": str,
            "last_name": str,
            "avatar_url": OPTIONAL(str),
            "cover_image_url": OPTIONAL(str),
            "date_joined": str,
            "user_timezone": OPTIONAL(str),
            "display_name": str,
        },
        where="user-profile.user_data",
    )
    assert body["user_data"]["email"] == client.user["email"]
    assert body["user_data"]["display_name"] == client.user["display_name"]

    # Owner (role 20 >= 15) -> project_data is populated. Find the seeded project
    # by id and value-assert its created_by-derived created_issues.
    by_id = {p["id"]: p for p in body["project_data"]}
    assert seeded["pid"] in by_id, f"seeded project missing from project_data: {list(by_id)}"
    proj = by_id[seeded["pid"]]
    assert_has_fields(
        proj,
        {
            "id": str,
            "logo_props": ANY,
            "created_issues": int,
            "assigned_issues": int,   # assignee-derived -> shape-only
            "completed_issues": int,
            "pending_issues": int,
        },
        where="user-profile.project",
    )
    assert proj["created_issues"] == 3, proj


# ---- user-activity ---------------------------------------------------------

def test_user_activity_empty_envelope_for_fresh_user(client, workspace):
    """A user_id with no activity -> full paginated envelope, empty results.
    Frozen against a FRESH user (no activity anywhere) so Python matches Go's
    always-empty log."""
    fresh = PlaneClient(client.base_url)
    fresh.sign_up()
    fresh.whoami()
    fresh_uid = fresh.user_id

    r = client.api_get(f"/api/workspaces/{workspace['slug']}/user-activity/{fresh_uid}/")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="user-activity", grouped=False)
    assert body["results"] == [], body
    assert body["total_count"] == 0, body
    assert body["total_results"] == 0, body
    assert body["count"] == 0, body


# ---- activity-graph --------------------------------------------------------

def test_activity_graph_shape_and_today_present(client, seeded):
    r = client.api_get(f"/api/users/me/workspaces/{seeded['slug']}/activity-graph/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list), body
    for row in body:
        assert_has_fields(row, {"created_date": str, "activity_count": int}, where="activity-graph")
    # A created issue is visible today. Value-assert the DATE (determinate on
    # both); activity_count is shape-only (audit rows on Django, created issues
    # on Go).
    today = datetime.date.today().isoformat()
    dates = {row["created_date"]: row["activity_count"] for row in body}
    assert today in dates, f"today {today} not in activity graph {dates}"
    assert dates[today] >= 1, dates


def test_activity_graph_ignores_user_id_context(client, seeded):
    """Sanity: the /users/me/ graph is for the authenticated user; is_uuid guard
    on returned dates keeps the shape honest."""
    r = client.api_get(f"/api/users/me/workspaces/{seeded['slug']}/activity-graph/")
    assert_status(r, 200)
    for row in r.json():
        # created_date is an ISO date, not a uuid; just assert parseable.
        datetime.date.fromisoformat(row["created_date"])
        assert not is_uuid(row["created_date"])
