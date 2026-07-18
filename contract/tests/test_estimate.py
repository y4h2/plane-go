"""Contract: project estimates (with nested estimate points).

Create returns 200 (not 201) — a deliberate quirk shared with state.
"""

import pytest

from lib.client import unique
from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.estimate


def _create(client, workspace, project):
    base = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/estimates/"
    r = client.api_post(
        base,
        json={
            "estimate": {"name": "E " + unique(), "type": "points"},
            "estimate_points": [{"key": 0, "value": "1"}, {"key": 1, "value": "2"}],
        },
    )
    return base, r


def test_create_estimate(client, workspace, project):
    _, r = _create(client, workspace, project)
    assert_status(r, 200)  # quirk: 200, not 201
    e = r.json()
    assert_has_fields(e, {"id": str, "name": str, "type": str, "points": list, "project": str}, where="estimate")
    assert is_uuid(e["id"])
    assert len(e["points"]) == 2
    for pt in e["points"]:
        assert_has_fields(pt, {"id": str, "key": int, "value": str}, where="estimate.point")


def test_list_estimates(client, workspace, project):
    base, r = _create(client, workspace, project)
    eid = r.json()["id"]
    rl = client.api_get(base)
    assert_status(rl, 200)
    assert any(e["id"] == eid for e in rl.json())


def test_retrieve_estimate(client, workspace, project):
    base, r = _create(client, workspace, project)
    eid = r.json()["id"]
    rr = client.api_get(base + f"{eid}/")
    assert_status(rr, 200)
    assert rr.json()["id"] == eid
