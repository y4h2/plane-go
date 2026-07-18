"""Contract: user-extras -- change-password and account deactivation.

Django sources:
- Change password: plane/authentication/urls.py -> ChangePasswordEndpoint
  (plane/authentication/views/common.py). NOTE: despite living under a "user"
  concern, this is mounted at plain `/auth/change-password/`, NOT under
  `/api/users/me/...` -- there is no `/api/users/me/change-password/` route
  at all (confirmed 404 against the reference). It also requires a CSRF
  token (double-submit `csrftoken` cookie + `X-CSRFToken` header) because it
  is a plain DRF APIView using default SessionAuthentication, unlike the
  `/api/*` ViewSets which don't enforce CSRF.
- Deactivate: plane/app/urls/user.py -> UserEndpoint.deactivate (DELETE
  /api/users/me/), plane/app/views/user/base.py.

Quirks pinned here:
- change-password success -> 200 {"message": "Password updated successfully"}.
  The session is *rotated* (old session-id cookie stops working, a new one is
  issued and swapped in transparently) -- mirrors Django's login() cycling the
  session key on password change.
- change-password missing old_password -> 400 error_code 5138 MISSING_PASSWORD,
  "Old password is missing".
- change-password missing new_password (old present) -> 400 error_code 5138
  MISSING_PASSWORD, "Old or new password is missing" (different message).
- change-password wrong old_password -> 400 error_code 5135
  INCORRECT_OLD_PASSWORD, "Old password is not correct".
- change-password weak new_password -> 400 error_code 5021 PASSWORD_TOO_WEAK
  (no "error" key in the payload, unlike the other error shapes).
- change-password without a CSRF token -> 403 {"detail": "CSRF Failed: CSRF
  token missing."} even when authenticated.
- change-password unauthenticated (no session at all) -> 401, takes priority
  over the CSRF check.
- deactivate success -> 204, empty body, and an explicit Set-Cookie clearing
  session-id (Max-Age=0). All sessions for the user are invalidated (old
  cookie -> 401 on the next request) and the password is scrambled so the old
  credentials can no longer sign in.
- deactivate unauthenticated -> 401.

CAUTION: deactivate permanently disables the account. Every deactivate test
below signs up its own dedicated throwaway user (via `fresh_client` / a fresh
`PlaneClient().sign_up()`) so it never touches shared fixture state.
"""

import requests

from lib.client import STRONG_PASSWORD, PlaneClient, unique

import pytest

pytestmark = pytest.mark.user_extras


def csrf_headers(c: PlaneClient) -> dict:
    """Fetch a fresh CSRF token and return headers for a JSON POST that needs it.

    /auth/change-password/ enforces Django's double-submit CSRF check even for
    JSON bodies: the `csrftoken` cookie (set by /auth/get-csrf-token/) must
    match the `X-CSRFToken` header.
    """
    token = c.csrf_token()
    return {"X-CSRFToken": token, "Referer": f"{c.base_url}/"}


# ---- change-password -------------------------------------------------------


def test_change_password_wrong_route_is_404(base_url):
    # Pin the surprise: there is no /api/users/me/change-password/ route.
    c = PlaneClient(base_url).sign_up()
    r = c.api_post("/api/users/me/change-password/", json={})
    assert r.status_code == 404


def test_change_password_success_rotates_session(base_url):
    c = PlaneClient(base_url).sign_up()
    old_cookie = c.session.cookies.get("session-id")
    new_pw = "Zx9#wQmv2Lp8rand"

    r = c.api_post(
        "/auth/change-password/",
        json={"old_password": STRONG_PASSWORD, "new_password": new_pw},
        headers=csrf_headers(c),
    )
    assert r.status_code == 200, r.text
    assert r.json() == {"message": "Password updated successfully"}

    # session cookie is rotated: a new session-id is now set...
    new_cookie = c.session.cookies.get("session-id")
    assert new_cookie and new_cookie != old_cookie

    # ...the new one keeps working (client.session picks up the Set-Cookie
    # automatically),
    assert c.api_get("/api/users/me/").status_code == 200

    # ...but the OLD session-id no longer authenticates.
    r_old = requests.get(f"{base_url}/api/users/me/", cookies={"session-id": old_cookie}, timeout=30)
    assert r_old.status_code == 401

    # the new password actually works for a fresh sign-in, and the old one no
    # longer does.
    fresh = PlaneClient(base_url)
    fresh.sign_in(c.email, new_pw)
    assert fresh.whoami()["email"] == c.email

    with pytest.raises(Exception):
        PlaneClient(base_url).sign_in(c.email, STRONG_PASSWORD)


def test_change_password_missing_old_password(base_url):
    c = PlaneClient(base_url).sign_up()
    r = c.api_post(
        "/auth/change-password/",
        json={"new_password": "Another9#Strong"},
        headers=csrf_headers(c),
    )
    assert r.status_code == 400
    body = r.json()
    assert body["error_code"] == 5138
    assert body["error_message"] == "MISSING_PASSWORD"
    assert body["error"] == "Old password is missing"


def test_change_password_missing_new_password(base_url):
    c = PlaneClient(base_url).sign_up()
    r = c.api_post(
        "/auth/change-password/",
        json={"old_password": STRONG_PASSWORD},
        headers=csrf_headers(c),
    )
    assert r.status_code == 400
    body = r.json()
    assert body["error_code"] == 5138
    assert body["error_message"] == "MISSING_PASSWORD"
    assert body["error"] == "Old or new password is missing"


def test_change_password_wrong_old_password(base_url):
    c = PlaneClient(base_url).sign_up()
    r = c.api_post(
        "/auth/change-password/",
        json={"old_password": "totally-wrong-pw", "new_password": "Another9#Strong"},
        headers=csrf_headers(c),
    )
    assert r.status_code == 400
    body = r.json()
    assert body["error_code"] == 5135
    assert body["error_message"] == "INCORRECT_OLD_PASSWORD"
    assert body["error"] == "Old password is not correct"

    # unaffected: old password still works.
    fresh = PlaneClient(base_url)
    fresh.sign_in(c.email, STRONG_PASSWORD)


def test_change_password_weak_new_password(base_url):
    c = PlaneClient(base_url).sign_up()
    r = c.api_post(
        "/auth/change-password/",
        json={"old_password": STRONG_PASSWORD, "new_password": "password"},
        headers=csrf_headers(c),
    )
    assert r.status_code == 400
    body = r.json()
    assert body["error_code"] == 5021
    assert body["error_message"] == "PASSWORD_TOO_WEAK"
    assert "error" not in body


def test_change_password_missing_csrf_token(base_url):
    c = PlaneClient(base_url).sign_up()
    r = c.api_post(
        "/auth/change-password/",
        json={"old_password": STRONG_PASSWORD, "new_password": "Another9#Strong"},
    )
    assert r.status_code == 403
    assert "CSRF" in r.json()["detail"]

    # unaffected: original password still works.
    fresh = PlaneClient(base_url)
    fresh.sign_in(c.email, STRONG_PASSWORD)


def test_change_password_unauthenticated(base_url):
    r = requests.post(
        f"{base_url}/auth/change-password/",
        json={"old_password": "x", "new_password": "y"},
        timeout=30,
    )
    assert r.status_code == 401
    assert r.json() == {"detail": "Authentication credentials were not provided."}


# ---- deactivate -------------------------------------------------------------


def test_deactivate_unauthenticated(base_url):
    r = requests.delete(f"{base_url}/api/users/me/", timeout=30)
    assert r.status_code == 401


def test_deactivate_success(base_url):
    # Dedicated throwaway user -- never reuse a shared fixture client here.
    c = PlaneClient(base_url).sign_up()
    email, pw = c.email, STRONG_PASSWORD
    old_cookie = c.session.cookies.get("session-id")

    r = c.api_delete("/api/users/me/")
    assert r.status_code == 204
    assert r.text == ""

    # The session-id cookie is explicitly cleared in the response...
    set_cookie = r.headers.get("Set-Cookie", "")
    assert "session-id=" in set_cookie

    # ...and the old session no longer authenticates anywhere.
    r_old = requests.get(f"{base_url}/api/users/me/", cookies={"session-id": old_cookie}, timeout=30)
    assert r_old.status_code == 401

    # The deactivated account can no longer sign in with its old credentials.
    with pytest.raises(Exception):
        PlaneClient(base_url).sign_in(email, pw)


def test_deactivate_does_not_affect_other_users(base_url):
    victim = PlaneClient(base_url).sign_up()
    bystander = PlaneClient(base_url).sign_up()

    r = victim.api_delete("/api/users/me/")
    assert r.status_code == 204

    # the bystander's own session is untouched.
    assert bystander.whoami()["email"] == bystander.email
