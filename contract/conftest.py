"""Shared fixtures. Everything is created via the API so the suite stays black-box
and server-agnostic (Python reference today, Go port tomorrow)."""

import pytest

from lib.client import PlaneClient


@pytest.fixture(scope="session")
def base_url() -> str:
    import os

    return os.environ.get("BASE_URL", "http://localhost:8000").rstrip("/")


@pytest.fixture(scope="session")
def client(base_url) -> PlaneClient:
    """A signed-in client, shared across the session (read-heavy tests reuse it)."""
    c = PlaneClient(base_url)
    c.sign_up()
    c.whoami()
    return c


@pytest.fixture
def fresh_client(base_url) -> PlaneClient:
    """A brand-new signed-in client for tests needing an isolated user/session."""
    c = PlaneClient(base_url)
    c.sign_up()
    c.whoami()
    return c


@pytest.fixture
def workspace(client) -> dict:
    """A fresh workspace owned by `client`. No teardown needed (unique per test)."""
    return client.create_workspace()


@pytest.fixture
def project(client, workspace) -> dict:
    """A fresh project inside `workspace`."""
    return client.create_project(workspace["slug"])
