"""Contract: boot / auth-extra endpoints the frontend hits on load and during
onboarding.

Covers email-check (pre-auth existence probe), the unauthenticated instance
config endpoint, and the users/me + profile + onboarding-flag PATCH family.

QUIRKS confirmed live against the Python reference:
- `POST /auth/email-check/` is form-encoded (like sign-up/sign-in), requires a
  CSRF token + Referer header, and works pre-auth. Response is always 200
  `{"existing": bool, "status": "CREDENTIAL"}` regardless of whether the email
  exists.
- `GET /api/instances/` requires **no** auth at all (no session cookie) — the
  frontend calls it before login to decide which auth methods to show.
- `PATCH /api/users/me/` echoes the full user object (not just the patched
  fields).
- `PATCH /api/users/me/onboard/` and `PATCH /api/users/me/tour-completed/`
  both return the same generic `{"message": "Updated successfully"}` shape
  rather than echoing the profile.
- `is_tour_completed` set via `PATCH /api/users/me/profile/` persists: a
  follow-up `GET` reflects it.
"""

import pytest

from lib.client import PlaneClient, unique
from lib.shape import assert_has_fields, assert_status

pytestmark = pytest.mark.boot


def _email_check(c: PlaneClient, base_url: str, email: str):
    token = c.csrf_token()
    return c.session.post(
        f"{base_url}/auth/email-check/",
        data={"csrfmiddlewaretoken": token, "email": email},
        headers={"Referer": f"{base_url}/"},
        timeout=30,
    )


def test_email_check_fresh_email_not_existing(base_url):
    c = PlaneClient(base_url)
    r = _email_check(c, base_url, f"{unique('ct-')}@plane.test")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"existing": bool, "status": str}, where="email-check")
    assert body["existing"] is False
    assert body["status"] == "CREDENTIAL"


def test_email_check_existing_user(base_url):
    signer = PlaneClient(base_url).sign_up()
    c = PlaneClient(base_url)
    r = _email_check(c, base_url, signer.email)
    assert_status(r, 200)
    body = r.json()
    assert body["existing"] is True
    assert body["status"] == "CREDENTIAL"


def test_instances_config_unauthenticated(base_url):
    # Fresh client with no sign-up / sign-in at all: this endpoint must work
    # pre-auth so the frontend can render the login screen.
    c = PlaneClient(base_url)
    r = c.api_get("/api/instances/")
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"config": dict, "instance": dict}, where="instances")
    assert_has_fields(
        body["config"],
        {"is_email_password_enabled": bool, "app_base_url": str},
        where="instances.config",
    )
    assert_has_fields(body["instance"], {"is_setup_done": bool}, where="instances.instance")


def test_patch_users_me_updates_name(client):
    r = client.api_patch("/api/users/me/", json={"first_name": "Ada", "last_name": "Lovelace"})
    assert_status(r, 200)
    body = r.json()
    assert_has_fields(body, {"id": str, "email": str, "first_name": str, "last_name": str}, where="users-me")
    assert body["first_name"] == "Ada"
    assert body["last_name"] == "Lovelace"


def test_profile_get_and_patch_tour_completed_persists(fresh_client):
    r = fresh_client.api_get("/api/users/me/profile/")
    assert_status(r, 200)
    assert_has_fields(r.json(), {"id": str, "is_tour_completed": bool, "is_onboarded": bool}, where="profile")

    r = fresh_client.api_patch("/api/users/me/profile/", json={"is_tour_completed": True})
    assert_status(r, 200)
    assert r.json()["is_tour_completed"] is True

    # re-fetch to prove the flag was actually persisted server-side, not just echoed
    r = fresh_client.api_get("/api/users/me/profile/")
    assert_status(r, 200)
    assert r.json()["is_tour_completed"] is True


def test_patch_onboard(fresh_client):
    r = fresh_client.api_patch("/api/users/me/onboard/", json={"is_onboarded": True})
    assert_status(r, 200)
    assert_has_fields(r.json(), {"message": str}, where="onboard")


def test_patch_tour_completed_message(fresh_client):
    r = fresh_client.api_patch("/api/users/me/tour-completed/", json={"is_tour_completed": True})
    assert_status(r, 200)
    assert r.json() == {"message": "Updated successfully"}
