"""Contract: Unsplash + AI-assistant integration endpoints
(Django: urls/external.py -> UnsplashEndpoint, GPTIntegrationEndpoint,
WorkspaceGPTIntegrationEndpoint).

This environment has no UNSPLASH_ACCESS_KEY or LLM_API_KEY configured. These
tests freeze what the Django reference actually returns in that unconfigured
state — they never reach a real third-party API, and are not testing
Unsplash/OpenAI/Gemini behavior itself:

  - GET /unsplash/ short-circuits to 200 [] before building any outbound
    request, regardless of query params.
  - Both ai-assistant endpoints check the configured LLM key/model *before*
    validating "task"/"prompt", so an unconfigured key always yields 400
    with the same error body regardless of what's posted.
"""

import pytest

from lib.shape import assert_status

pytestmark = pytest.mark.external

LLM_UNCONFIGURED_ERROR = {"error": "LLM provider API key and model are required"}


def test_unsplash_no_query(client):
    r = client.api_get("/api/unsplash/")
    assert_status(r, 200)
    assert r.json() == []


def test_unsplash_with_query(client):
    r = client.api_get("/api/unsplash/?query=office&page=1&per_page=5")
    assert_status(r, 200)
    assert r.json() == []


def test_workspace_ai_assistant_unconfigured(client, workspace):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/ai-assistant/",
        json={"task": "summarize", "prompt": "hello"},
    )
    assert_status(r, 400)
    assert r.json() == LLM_UNCONFIGURED_ERROR


def test_workspace_ai_assistant_unconfigured_no_task(client, workspace):
    r = client.api_post(f"/api/workspaces/{workspace['slug']}/ai-assistant/", json={})
    assert_status(r, 400)
    assert r.json() == LLM_UNCONFIGURED_ERROR


def test_project_ai_assistant_unconfigured(client, workspace, project):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/ai-assistant/",
        json={"task": "summarize", "prompt": "hello"},
    )
    assert_status(r, 400)
    assert r.json() == LLM_UNCONFIGURED_ERROR


def test_project_ai_assistant_unconfigured_no_task(client, workspace, project):
    r = client.api_post(
        f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/ai-assistant/", json={}
    )
    assert_status(r, 400)
    assert r.json() == LLM_UNCONFIGURED_ERROR
