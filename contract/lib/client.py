"""PlaneClient — a black-box HTTP client for Plane's app API.

Implements the real browser auth flow (get-csrf -> sign-up/sign-in -> `session-id`
cookie held in the requests.Session cookie jar), then exposes thin `api_*` helpers.
No knowledge of the server implementation — only HTTP. The same client therefore
drives the Python reference and the Go port.
"""

import os
import uuid

import requests

# A password that clears Plane's zxcvbn-style strength check (>= 8 chars, not a
# common/patterned password). Kept constant so tests are deterministic.
STRONG_PASSWORD = "qK7#mvZ2rLpX9wander"


def unique(prefix: str = "") -> str:
    """Short unique token safe for emails, slugs (regex ^[a-zA-Z0-9_-]+$) and names."""
    return f"{prefix}{uuid.uuid4().hex[:12]}"


class AuthError(AssertionError):
    pass


class PlaneClient:
    def __init__(self, base_url: str | None = None):
        self.base_url = (base_url or os.environ.get("BASE_URL", "http://localhost:8000")).rstrip("/")
        self.session = requests.Session()
        self.email: str | None = None
        self.user: dict | None = None

    # ---- auth ---------------------------------------------------------------
    def csrf_token(self) -> str:
        # Always fetch fresh: Django rotates the CSRF token on login, so a token
        # cached before sign-up/sign-in would fail the CSRF check on a later
        # /auth/ POST (e.g. sign-out). Only auth actions call this, so the load
        # is ~1 per client; the reference's anon throttle is reset per run by the
        # Makefile. /api/ data endpoints don't use CSRF at all.
        r = self.session.get(f"{self.base_url}/auth/get-csrf-token/", timeout=30)
        assert r.status_code == 200, f"get-csrf-token -> {r.status_code}: {r.text[:200]}"
        return r.json()["csrf_token"]

    def _auth_form_post(self, path: str, email: str, password: str) -> requests.Response:
        token = self.csrf_token()
        # Django form auth: csrf token as a hidden field, validated against the cookie
        # the session jar now holds. Referer is required by Django's CSRF check.
        r = self.session.post(
            f"{self.base_url}{path}",
            data={"csrfmiddlewaretoken": token, "email": email, "password": password},
            headers={"Referer": f"{self.base_url}/"},
            allow_redirects=False,
            timeout=30,
        )
        return r

    def sign_up(self, email: str | None = None, password: str = STRONG_PASSWORD) -> "PlaneClient":
        email = email or f"{unique('ct-')}@plane.test"
        r = self._auth_form_post("/auth/sign-up/", email, password)
        loc = r.headers.get("Location", "")
        assert r.status_code in (302, 303), f"sign-up -> {r.status_code}: {r.text[:200]}"
        if "error_code" in loc:
            raise AuthError(f"sign-up failed: {loc}")
        assert "session-id" in self.session.cookies, f"sign-up set no session cookie (loc={loc})"
        self.email = email
        return self

    def sign_in(self, email: str, password: str = STRONG_PASSWORD) -> "PlaneClient":
        r = self._auth_form_post("/auth/sign-in/", email, password)
        loc = r.headers.get("Location", "")
        assert r.status_code in (302, 303), f"sign-in -> {r.status_code}: {r.text[:200]}"
        if "error_code" in loc:
            raise AuthError(f"sign-in failed: {loc}")
        assert "session-id" in self.session.cookies, f"sign-in set no session cookie (loc={loc})"
        self.email = email
        return self

    def sign_out(self) -> requests.Response:
        token = self.csrf_token()
        return self.session.post(
            f"{self.base_url}/auth/sign-out/",
            data={"csrfmiddlewaretoken": token},
            headers={"Referer": f"{self.base_url}/"},
            allow_redirects=False,
            timeout=30,
        )

    def whoami(self) -> dict:
        r = self.api_get("/api/users/me/")
        assert r.status_code == 200, f"users/me -> {r.status_code}"
        self.user = r.json()
        return self.user

    @property
    def user_id(self) -> str:
        if self.user is None:
            self.whoami()
        return self.user["id"]

    # ---- raw HTTP helpers (path is absolute from host, e.g. /api/workspaces/) ----
    def api_get(self, path: str, **kw) -> requests.Response:
        return self.session.get(f"{self.base_url}{path}", timeout=30, **kw)

    def api_post(self, path: str, json=None, **kw) -> requests.Response:
        return self.session.post(f"{self.base_url}{path}", json=json, timeout=30, **kw)

    def api_patch(self, path: str, json=None, **kw) -> requests.Response:
        return self.session.patch(f"{self.base_url}{path}", json=json, timeout=30, **kw)

    def api_put(self, path: str, json=None, **kw) -> requests.Response:
        return self.session.put(f"{self.base_url}{path}", json=json, timeout=30, **kw)

    def api_delete(self, path: str, **kw) -> requests.Response:
        return self.session.delete(f"{self.base_url}{path}", timeout=30, **kw)

    # ---- convenience builders used by fixtures ------------------------------
    def create_workspace(self) -> dict:
        slug = unique("ws")
        r = self.api_post("/api/workspaces/", json={"name": "CT WS", "slug": slug, "organization_size": "1-10"})
        assert r.status_code == 201, f"create workspace -> {r.status_code}: {r.text[:300]}"
        return r.json()

    def create_project(self, slug: str) -> dict:
        r = self.api_post(
            f"/api/workspaces/{slug}/projects/",
            json={"name": f"CT {unique()}", "identifier": unique("P")[:8].upper()},
        )
        assert r.status_code == 201, f"create project -> {r.status_code}: {r.text[:300]}"
        return r.json()
