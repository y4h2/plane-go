"""Contract: notification snooze (PATCH .../notifications/<pk>/) and the
remaining per-id notification actions (mark-read, mark-unread, archive,
unarchive).

Snooze has no dedicated URL in the Django reference — it rides the existing
`partial_update` route (`PATCH /users/notifications/<pk>/`), which the
serializer restricts to the `snoozed_till` field (see
apps/api/plane/app/views/notification/base.py::NotificationViewSet.partial_update).

Neither backend has any notification reachable through this black-box client:
notifications are produced by an async signal/webhook pipeline this suite
never triggers, and the Go port has no notification-generation subsystem at
all. So a real snooze round-trip (set `snoozed_till` on a genuine
notification, observe it move in/out of the `snoozed` list filter) is not
testable here.

What IS deterministic on both backends, verified against the Python
reference: every per-id notification action does an ownership lookup
(`Notification.objects.get(workspace__slug=slug, pk=pk, receiver=request.user)`)
*before* touching the request body, and a syntactically valid but nonexistent
id raises Django's `ObjectDoesNotExist`, which the shared `BaseViewSet`
exception handler turns into a pinned 404 contract:
`{"error": "The required object does not exist."}` — not a 404-for-unmatched-route,
and not a silent 200/204. That 404 is what this suite freezes: it proves the
route exists, is wired to the real per-id handler (not a generic stub), and
enforces ownership ahead of any body validation.
"""

import uuid

import pytest

from lib.shape import assert_status

pytestmark = pytest.mark.notification_snooze

NOT_FOUND_BODY = {"error": "The required object does not exist."}


def _notification_url(slug: str, notif_id: str) -> str:
    return f"/api/workspaces/{slug}/users/notifications/{notif_id}/"


def _action_url(slug: str, notif_id: str, action: str) -> str:
    return f"/api/workspaces/{slug}/users/notifications/{notif_id}/{action}/"


def test_snooze_nonexistent_notification_404(client, workspace):
    """PATCH with a future snoozed_till on an unknown id -> 404, not 200/204."""
    fake_id = str(uuid.uuid4())
    r = client.api_patch(
        _notification_url(workspace["slug"], fake_id),
        json={"snoozed_till": "2099-01-01T00:00:00Z"},
    )
    assert_status(r, 404)
    assert r.json() == NOT_FOUND_BODY
    assert r.headers.get("content-type", "").startswith("application/json")


def test_snooze_clear_nonexistent_notification_404(client, workspace):
    """PATCH with snoozed_till: null (un-snooze) on an unknown id -> same 404 contract."""
    fake_id = str(uuid.uuid4())
    r = client.api_patch(
        _notification_url(workspace["slug"], fake_id),
        json={"snoozed_till": None},
    )
    assert_status(r, 404)
    assert r.json() == NOT_FOUND_BODY


def test_snooze_empty_body_nonexistent_notification_404(client, workspace):
    """Ownership lookup happens before body validation: an empty body still 404s
    (not a 400), because the id doesn't exist regardless of what's sent."""
    fake_id = str(uuid.uuid4())
    r = client.api_patch(_notification_url(workspace["slug"], fake_id), json={})
    assert_status(r, 404)
    assert r.json() == NOT_FOUND_BODY


def test_snooze_route_distinct_from_unmatched_route(client, workspace):
    """Sanity check that the notification-detail route is actually registered
    (this is an ownership 404 with a JSON error body), as opposed to a path
    that was never wired up at all."""
    fake_id = str(uuid.uuid4())
    r = client.api_patch(
        _notification_url(workspace["slug"], fake_id),
        json={"snoozed_till": "2099-01-01T00:00:00Z"},
    )
    assert r.status_code == 404
    assert r.json() == NOT_FOUND_BODY  # the pinned ownership-404 shape


@pytest.mark.parametrize("method,action", [("post", "read"), ("delete", "read"), ("post", "archive"), ("delete", "archive")])
def test_notification_action_nonexistent_404(client, workspace, method, action):
    """mark_read (POST .../read/), mark_unread (DELETE .../read/), archive
    (POST .../archive/), and unarchive (DELETE .../archive/) all share the
    same get-before-act pattern and therefore the same 404 contract."""
    fake_id = str(uuid.uuid4())
    url = _action_url(workspace["slug"], fake_id, action)
    r = getattr(client, f"api_{method}")(url)
    assert_status(r, 404)
    assert r.json() == NOT_FOUND_BODY
