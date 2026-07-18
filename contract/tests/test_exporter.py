"""Contract: workspace issue export (Django: urls/exporter.py -> ExportIssuesEndpoint).

POST enqueues an export job (provider in csv|xlsx|json) and always returns the
same fire-and-forget message — the reference hands the real work to a celery
task, so "status" is whatever the background worker has gotten to by the time
we read it back (queued/processing/completed/failed all observed in practice).
We therefore only pin that it's a non-empty string from the known enum, not
its exact value.

GET requires both `per_page` and `cursor` (a quirk shared with a few other
list endpoints in this codebase) and otherwise returns the same cursor-
pagination envelope as the issue family.
"""

import pytest

from lib.shape import assert_envelope, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.exporter

KNOWN_STATUSES = {"queued", "processing", "completed", "failed"}


def _base(workspace):
    return f"/api/workspaces/{workspace['slug']}/export-issues/"


def test_export_create_csv(client, workspace):
    r = client.api_post(_base(workspace), json={"provider": "csv"})
    assert_status(r, 200)
    assert r.json() == {"message": "Once the export is ready you will be able to download it"}


def test_export_create_xlsx(client, workspace):
    r = client.api_post(_base(workspace), json={"provider": "xlsx"})
    assert_status(r, 200)
    assert r.json() == {"message": "Once the export is ready you will be able to download it"}


def test_export_create_invalid_provider(client, workspace):
    r = client.api_post(_base(workspace), json={"provider": "bogus"})
    assert_status(r, 400)
    assert r.json() == {"error": "Provider 'bogus' not found."}


def test_export_create_missing_provider(client, workspace):
    # Quirk: Django's `request.data.get("provider", False)` renders the
    # missing-key default (Python's `False`) straight into the message.
    r = client.api_post(_base(workspace), json={})
    assert_status(r, 400)
    assert r.json() == {"error": "Provider 'False' not found."}


def test_export_list_requires_pagination_params(client, workspace):
    r = client.api_get(_base(workspace))
    assert_status(r, 400)
    assert r.json() == {"error": "per_page and cursor are required"}


def test_export_list_paginated(client, workspace, project):
    create = client.api_post(_base(workspace), json={"provider": "json"})
    assert_status(create, 200)

    r = client.api_get(_base(workspace) + "?per_page=10&cursor=10:0:0")
    assert_status(r, 200)
    body = r.json()
    assert_envelope(body, where="export-issues-list")
    assert body["total_count"] >= 1
    assert len(body["results"]) >= 1

    job = body["results"][-1]
    assert_has_fields(
        job,
        {
            "id": str,
            "created_at": str,
            "updated_at": str,
            "project": list,
            "provider": str,
            "status": str,
            "initiated_by": str,
            "initiated_by_detail": dict,
            "token": str,
        },
        where="export-job",
    )
    assert is_uuid(job["id"])
    assert is_uuid(job["initiated_by"])
    assert job["status"] in KNOWN_STATUSES
    assert job["provider"] in {"csv", "xlsx", "json"}
    for pid in job["project"]:
        assert is_uuid(pid)

    detail = job["initiated_by_detail"]
    assert_has_fields(
        detail,
        {
            "id": str,
            "first_name": str,
            "last_name": str,
            "avatar": str,
            "is_bot": bool,
            "display_name": str,
        },
        where="export-job.initiated_by_detail",
    )
    assert detail["id"] == job["initiated_by"]


def test_export_list_defaults_project_to_member_projects(client, workspace, project):
    """No explicit `project` in the body -> defaults to the caller's active
    projects in the workspace, which includes the `project` fixture."""
    create = client.api_post(_base(workspace), json={"provider": "csv"})
    assert_status(create, 200)

    r = client.api_get(_base(workspace) + "?per_page=50&cursor=50:0:0")
    assert_status(r, 200)
    results = r.json()["results"]
    assert any(project["id"] in job["project"] for job in results)
