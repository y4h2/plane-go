"""Contract: workspace search, entity-search (@mention), project-stats,
estimate update/delete, and project archive/unarchive.

All fields/behaviors below were probed live against the Python reference
(localhost:8000) before being asserted here — this file freezes what Python
actually returns, not the spec as originally imagined. Notably:

- `GET .../search/?search=` (empty string) does NOT filter — the view does
  `query or None`, so an empty/omitted `search` param means "no filter,
  return everything the user can see", not "return nothing". Only a
  non-matching *non-empty* term yields empty lists.
"""

import pytest

from lib.client import unique
from lib.shape import OPTIONAL, assert_fields, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.search_estimate_archive

SEARCH_ENVELOPE_KEYS = {
    "workspace": list,
    "project": list,
    "issue": list,
    "cycle": list,
    "module": list,
    "issue_view": list,
    "page": list,
    "intake": list,
}

ISSUE_HIT_FIELDS = {
    "name": str,
    "id": str,
    "sequence_id": int,
    "project__identifier": str,
    "project_id": str,
    "workspace__slug": str,
}


def _create_issue(client, workspace, project, name):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/issues/",
        json={"name": name},
    )
    assert_status(r, 201)
    return r.json()


# ---- 1. workspace search --------------------------------------------------


def test_workspace_search_finds_issue(client, workspace, project):
    name = "SRCH " + unique()
    issue = _create_issue(client, workspace, project, name)

    r = client.api_get(f"/api/workspaces/{workspace['slug']}/search/", params={"search": name})
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"results": dict}, where="search")
    results = body["results"]
    assert_fields(results, SEARCH_ENVELOPE_KEYS, where="search.results")

    hits = results["issue"]
    assert len(hits) == 1, f"expected exactly 1 issue hit, got {hits}"
    hit = hits[0]
    assert_fields(hit, ISSUE_HIT_FIELDS, where="search.results.issue[0]")
    assert hit["id"] == issue["id"]
    assert hit["name"] == name
    assert hit["project_id"] == project["id"]
    assert hit["project__identifier"] == project["identifier"]
    assert hit["workspace__slug"] == workspace["slug"]
    assert is_uuid(hit["id"])
    assert is_uuid(hit["project_id"])

    # unrelated entity buckets stay empty for a term that only matches the issue
    assert results["cycle"] == []
    assert results["module"] == []


def test_workspace_search_no_match_returns_empty_lists(client, workspace, project):
    _create_issue(client, workspace, project, "SRCH " + unique())

    r = client.api_get(
        f"/api/workspaces/{workspace['slug']}/search/",
        params={"search": "zzzznomatchxyzqqqq"},
    )
    assert_status(r, 200)
    results = r.json()["results"]
    assert_fields(results, SEARCH_ENVELOPE_KEYS, where="search.results")
    for key, hits in results.items():
        assert hits == [], f"results.{key} expected empty for non-matching term, got {hits}"


def test_workspace_search_empty_term_is_unfiltered(client, workspace, project):
    """Quirk: an empty `search` value disables filtering entirely (see module
    docstring), so it returns the workspace's own entities, not empty lists."""
    issue = _create_issue(client, workspace, project, "SRCH " + unique())

    r = client.api_get(f"/api/workspaces/{workspace['slug']}/search/", params={"search": ""})
    assert_status(r, 200)
    results = r.json()["results"]
    assert_fields(results, SEARCH_ENVELOPE_KEYS, where="search.results")
    assert any(hit["id"] == issue["id"] for hit in results["issue"])
    assert any(hit["id"] == project["id"] for hit in results["project"])


# ---- 2. entity search (@mention) ------------------------------------------


def test_entity_search_user_mention(client, workspace, project):
    r = client.api_get(
        f"/api/workspaces/{workspace['slug']}/entity-search/",
        params={"query": "", "count": 5},
    )
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, {"user_mention": list}, where="entity-search")
    mentions = body["user_mention"]
    assert len(mentions) >= 1
    for m in mentions:
        assert_fields(
            m,
            {"member__display_name": str, "member__id": str, "member__avatar_url": OPTIONAL(str)},
            where="entity-search.user_mention[]",
        )
        assert is_uuid(m["member__id"])
    assert any(m["member__id"] == client.user_id for m in mentions)


def test_entity_search_respects_count(client, workspace, project):
    r = client.api_get(
        f"/api/workspaces/{workspace['slug']}/entity-search/",
        params={"query": "", "count": 1},
    )
    assert_status(r, 200)
    assert len(r.json()["user_mention"]) <= 1


# ---- 3. project stats -------------------------------------------------


def test_project_stats(client, workspace, project):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/project-stats/")
    assert_status(r, 200)
    stats = r.json()
    assert isinstance(stats, list)
    assert len(stats) >= 1

    row = next((s for s in stats if s["id"] == project["id"]), None)
    assert row is not None, f"project {project['id']} missing from project-stats: {stats}"
    assert_fields(
        row,
        {
            "id": str,
            "total_issues": int,
            "completed_issues": int,
            "total_cycles": int,
            "total_modules": int,
            "total_members": int,
        },
        where="project-stats[]",
    )
    assert is_uuid(row["id"])
    assert row["total_members"] >= 1


# ---- 4/5. estimate update + delete -----------------------------------------


def _create_estimate(client, workspace, project):
    base = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/estimates/"
    r = client.api_post(
        base,
        json={
            "estimate": {"name": "E " + unique(), "type": "points"},
            "estimate_points": [{"key": 0, "value": "1"}, {"key": 1, "value": "2"}],
        },
    )
    assert_status(r, 200)
    return base, r.json()


def test_estimate_update(client, workspace, project):
    base, estimate = _create_estimate(client, workspace, project)
    eid = estimate["id"]
    points = estimate["points"]
    assert len(points) == 2

    new_name = "Updated " + unique()
    r = client.api_patch(
        base + f"{eid}/",
        json={
            "estimate": {"name": new_name, "type": "categories"},
            "estimate_points": [
                {"id": points[0]["id"], "value": "5"},
                {"id": points[1]["id"], "value": "8"},
            ],
        },
    )
    assert_status(r, 200)
    updated = r.json()
    assert_has_fields(
        updated,
        {"id": str, "name": str, "type": str, "points": list, "project": str},
        where="estimate(updated)",
    )
    assert updated["id"] == eid
    assert updated["name"] == new_name
    assert updated["type"] == "categories"
    assert len(updated["points"]) == 2

    by_id = {p["id"]: p for p in updated["points"]}
    assert by_id[points[0]["id"]]["value"] == "5"
    assert by_id[points[1]["id"]]["value"] == "8"
    for pt in updated["points"]:
        assert_has_fields(pt, {"id": str, "key": int, "value": str}, where="estimate(updated).point")


def test_estimate_update_requires_estimate_points(client, workspace, project):
    base, estimate = _create_estimate(client, workspace, project)
    r = client.api_patch(base + f"{estimate['id']}/", json={"estimate": {"name": "no points"}})
    assert_status(r, 400)


def test_estimate_delete(client, workspace, project):
    base, estimate = _create_estimate(client, workspace, project)
    eid = estimate["id"]

    r = client.api_delete(base + f"{eid}/")
    assert_status(r, 204)

    rl = client.api_get(base)
    assert_status(rl, 200)
    assert all(e["id"] != eid for e in rl.json())


# ---- 6. project archive / unarchive ----------------------------------------


def test_project_archive_and_unarchive(client, workspace, project):
    pid = project["id"]
    archive_path = f"/api/workspaces/{workspace['slug']}/projects/{pid}/archive/"
    detail_path = f"/api/workspaces/{workspace['slug']}/projects/{pid}/"

    r = client.api_post(archive_path)
    assert_status(r, 200)
    body = r.json()
    assert_fields(body, {"archived_at": str}, where="archive")
    assert body["archived_at"]

    # Quirk: project-detail retrieve() filters archived_at__isnull=True, so an
    # archived project 404s from the normal detail endpoint.
    r_get = client.api_get(detail_path)
    assert_status(r_get, 404)

    r_unarchive = client.api_delete(archive_path)
    assert_status(r_unarchive, 204)

    r_get2 = client.api_get(detail_path)
    assert_status(r_get2, 200)
    assert r_get2.json()["archived_at"] is None
