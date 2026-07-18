"""Contract: authentication / session flow (/auth/*).

The frontend depends on: a CSRF token endpoint, form-urlencoded sign-up/sign-in that
302-redirect and set a `session-id` cookie, 401 on unauthenticated /api/*, and a
sign-out that clears the session.
"""

import pytest

from lib.client import PlaneClient, STRONG_PASSWORD, unique

pytestmark = pytest.mark.auth


def test_csrf_token_endpoint(base_url):
    c = PlaneClient(base_url)
    r = c.session.get(f"{base_url}/auth/get-csrf-token/")
    assert r.status_code == 200
    body = r.json()
    assert isinstance(body.get("csrf_token"), str) and body["csrf_token"]
    assert "csrftoken" in c.session.cookies


def test_unauthenticated_api_returns_401(base_url):
    c = PlaneClient(base_url)
    r = c.api_get("/api/users/me/")
    assert r.status_code == 401


def test_sign_up_sets_session_and_authenticates(base_url):
    c = PlaneClient(base_url)
    c.sign_up()
    assert "session-id" in c.session.cookies
    me = c.whoami()
    assert me["email"] == c.email
    assert me["is_active"] is True


def test_sign_up_weak_password_rejected(base_url):
    c = PlaneClient(base_url)
    r = c._auth_form_post("/auth/sign-up/", f"{unique('ct-')}@plane.test", "password")
    assert r.status_code in (302, 303)
    assert "error_code" in r.headers.get("Location", "")
    assert "session-id" not in c.session.cookies


def test_sign_in_existing_user(base_url):
    # sign up one client, then sign in as that user from a fresh client
    signer = PlaneClient(base_url).sign_up()
    email = signer.email

    c = PlaneClient(base_url)
    c.sign_in(email, STRONG_PASSWORD)
    assert "session-id" in c.session.cookies
    assert c.whoami()["email"] == email


def test_sign_in_wrong_password_rejected(base_url):
    signer = PlaneClient(base_url).sign_up()
    c = PlaneClient(base_url)
    r = c._auth_form_post("/auth/sign-in/", signer.email, "totallyWrong#9task")
    assert r.status_code in (302, 303)
    assert "error_code" in r.headers.get("Location", "")


def test_sign_out_clears_session(base_url):
    c = PlaneClient(base_url).sign_up()
    assert c.whoami()["email"] == c.email
    c.sign_out()
    # after sign-out the session must no longer authenticate
    assert c.api_get("/api/users/me/").status_code == 401
