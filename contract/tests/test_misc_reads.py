"""Contract: misc workspace/boot reads + notification actions.

Covers a grab-bag of endpoints the frontend hits on load / in the sidebar that
don't have a dedicated module yet: sidebar pin/sort preferences, workspace-level
user-properties (issue-view defaults), a project's auto-provisioned intake
("Triage") state, the global timezone list, workspace invitations, workspace
estimates, and the notification family (cursor-paginated list, bare list,
unread counts, mark-all-read).

Values are pinned only where they're structural (e.g. intake state's
`group == "triage"`); volatile/content values (timezone entries, notification
message text) are type-checked only, per the project's shape-not-value
convention.
"""

import pytest

from lib.shape import assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.misc_reads


def test_sidebar_preferences(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/sidebar-preferences/")
    assert_status(r, 200)
    body = r.json()
    assert isinstance(body, dict)
    assert len(body) >= 1, "expected at least one sidebar-preference entry"
    for key, entry in body.items():
        assert_has_fields(
            entry, {"is_pinned": bool, "sort_order": (int, float)}, where=f"sidebar-preferences[{key}]"
        )


def test_user_properties_workspace_level(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/user-properties/")
    assert_status(r, 200)
    assert_has_fields(
        r.json(),
        {"filters": dict, "display_filters": dict, "display_properties": dict},
        where="user-properties",
    )


def test_intake_state(client, workspace, project):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/intake-state/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"id": str, "name": str, "group": str}, where="intake-state")
    assert is_uuid(body["id"])
    assert body["group"] == "triage"


def test_timezones(client):
    r = client.api_get("/api/timezones/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"timezones": list}, where="timezones")
    tzs = body["timezones"]
    assert len(tzs) >= 1
    for tz in tzs:
        assert_has_fields(tz, {"value": str, "label": str}, where="timezones[]")


def test_invitations_list(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/invitations/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_estimates_list(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/estimates/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_notifications_cursor_paginated(client, workspace):
    # deliberately no trailing slash before the querystring
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/users/notifications?per_page=100&cursor=100:0:0")
    assert_status(r, 200)
    assert_has_fields(r.json(), {"results": list, "total_count": int}, where="notifications-cursor")


def test_notifications_bare_list(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/users/notifications/")
    assert_status(r, 200)
    assert isinstance(r.json(), list)


def test_notifications_unread_counts(client, workspace):
    r = client.api_get(f"/api/workspaces/{workspace['slug']}/users/notifications/unread/")
    assert_status(r, 200)
    assert_has_fields(
        r.json(),
        {"total_unread_notifications_count": int, "mention_unread_notifications_count": int},
        where="notifications-unread",
    )


def test_notifications_mark_all_read(client, workspace):
    r = client.api_post(f"/api/workspaces/{workspace['slug']}/users/notifications/mark-all-read/")
    assert_status(r, 200)
    # observed on the Python reference: {"message": "Successful"} — pin presence/type
    # only, not the exact wording, per this suite's shape-not-value convention.
    assert_has_fields(r.json(), {"message": str}, where="notifications-mark-all-read")
