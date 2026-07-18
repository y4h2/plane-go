"""Contract: workspace home-page reads — recent visits, quick links,
home preferences, and stickies."""

import pytest

from lib.shape import assert_envelope, assert_has_fields, assert_status

pytestmark = pytest.mark.workspace_home


def test_recent_visits(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/recent-visits/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_quick_links(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/quick-links/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_home_preferences(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/home-preferences/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, list) and len(body) >= 1
    for pref in body:
        assert_has_fields(pref, {"key": str, "is_enabled": bool, "sort_order": (int, float)}, where="home-pref")


def test_stickies_envelope(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/stickies/")
    assert_status(r, 200)
    assert_envelope(r.json(), where="stickies", grouped=False)
