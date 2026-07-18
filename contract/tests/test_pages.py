"""Contract: wiki/docs pages — the HTTP metadata CRUD surface.

The live collaborative binary editing (the `live` server, /description/ and
/versions/ endpoints) is out of scope; only the metadata endpoints that return
cleanly without the live container are pinned here.

Notable serializer quirks confirmed against the Python reference:
  * create (201) / retrieve (200) use PageDetailSerializer with the annotated
    queryset -> they carry is_favorite, label_ids, project_ids, description_html.
    retrieve additionally carries issue_ids.
  * list (200) uses PageSerializer -> is_favorite, label_ids, project_ids, but
    NO description_html.
  * update (200) serializes a plain (un-annotated) instance -> description_html
    is present, but is_favorite / label_ids / project_ids are NOT.
  * duplicate (201) is annotated with project_ids only -> description_html and
    project_ids present, is_favorite / label_ids absent.
"""

import pytest

from lib.client import unique
from lib.shape import ANY, OPTIONAL, assert_has_fields, assert_status, is_uuid

pytestmark = pytest.mark.pages


def _base(workspace, project):
    return f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/pages/"


def _create(client, workspace, project, **extra):
    base = _base(workspace, project)
    payload = {"name": "Page " + unique()}
    payload.update(extra)
    r = client.api_post(base, json=payload)
    return base, r


CORE = {
    "id": str,
    "name": str,
    "owned_by": str,
    "access": int,
    "color": str,
    "parent": OPTIONAL(str),
    "is_locked": bool,
    "archived_at": OPTIONAL(str),
    "workspace": str,
    "created_at": str,
    "updated_at": str,
    "created_by": OPTIONAL(str),
    "updated_by": OPTIONAL(str),
    "view_props": dict,
    "logo_props": dict,
}


def test_create_page(client, workspace, project):
    _, r = _create(client, workspace, project)
    assert_status(r, 201)
    p = r.json()
    assert_has_fields(
        p,
        {**CORE, "is_favorite": bool, "label_ids": list, "project_ids": list, "description_html": str},
        where="page",
    )
    assert is_uuid(p["id"])
    assert p["access"] == 0
    assert p["is_locked"] is False
    assert p["archived_at"] is None
    assert project["id"] in p["project_ids"]


def test_create_page_private_access(client, workspace, project):
    _, r = _create(client, workspace, project, access=1)
    assert_status(r, 201)
    assert r.json()["access"] == 1


def test_list_pages(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]
    rl = client.api_get(base)
    assert_status(rl, 200)
    body = rl.json()
    assert isinstance(body, list)
    mine = next((p for p in body if p["id"] == pid), None)
    assert mine is not None, "created page missing from list"
    assert_has_fields(
        mine,
        {**CORE, "is_favorite": bool, "label_ids": list, "project_ids": list},
        where="page.list",
    )
    # list uses PageSerializer (not detail): no description_html.
    assert "description_html" not in mine


def test_retrieve_page(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]
    rr = client.api_get(base + f"{pid}/")
    assert_status(rr, 200)
    p = rr.json()
    assert p["id"] == pid
    assert_has_fields(
        p,
        {
            **CORE,
            "is_favorite": bool,
            "label_ids": list,
            "project_ids": list,
            "description_html": str,
            "issue_ids": ANY,
        },
        where="page.detail",
    )


def test_update_page(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]
    ru = client.api_patch(base + f"{pid}/", json={"name": "Renamed", "color": "#ff0000"})
    assert_status(ru, 200)
    p = ru.json()
    assert p["name"] == "Renamed"
    assert p["color"] == "#ff0000"
    # update serializes a plain instance: description_html present, annotations absent.
    assert "description_html" in p
    assert "is_favorite" not in p
    assert "project_ids" not in p


def test_lock_unlock(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]

    rl = client.api_post(base + f"{pid}/lock/")
    assert_status(rl, 204)
    assert client.api_get(base + f"{pid}/").json()["is_locked"] is True

    # a locked page rejects metadata updates.
    rlk = client.api_patch(base + f"{pid}/", json={"name": "nope"})
    assert_status(rlk, 400)
    assert rlk.json()["error"] == "Page is locked"

    ru = client.api_delete(base + f"{pid}/lock/")
    assert_status(ru, 204)
    assert client.api_get(base + f"{pid}/").json()["is_locked"] is False


def test_access_endpoint(client, workspace, project):
    base, r = _create(client, workspace, project, access=0)
    pid = r.json()["id"]
    ra = client.api_post(base + f"{pid}/access/", json={"access": 1})
    assert_status(ra, 204)
    assert client.api_get(base + f"{pid}/").json()["access"] == 1


def test_favorite_unfavorite(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]
    fav_url = f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/favorite-pages/{pid}/"

    rf = client.api_post(fav_url)
    assert_status(rf, 204)
    assert client.api_get(base + f"{pid}/").json()["is_favorite"] is True

    ru = client.api_delete(fav_url)
    assert_status(ru, 204)
    assert client.api_get(base + f"{pid}/").json()["is_favorite"] is False


def test_duplicate_page(client, workspace, project):
    base, r = _create(client, workspace, project, color="#123456")
    pid = r.json()["id"]
    rd = client.api_post(base + f"{pid}/duplicate/")
    assert_status(rd, 201)
    dup = rd.json()
    assert is_uuid(dup["id"])
    assert dup["id"] != pid
    assert dup["name"].endswith("(Copy)")
    assert dup["color"] == "#123456"
    # duplicate is annotated with project_ids only.
    assert "description_html" in dup
    assert "project_ids" in dup
    assert "is_favorite" not in dup


def test_summary(client, workspace, project):
    base, _ = _create(client, workspace, project, access=0)
    _create(client, workspace, project, access=1)
    rs = client.api_get(f"/api/workspaces/{workspace['slug']}/projects/{project['id']}/pages-summary/")
    assert_status(rs, 200)
    s = rs.json()
    assert_has_fields(
        s,
        {"public_pages": int, "private_pages": int, "archived_pages": int},
        where="summary",
    )
    assert s["public_pages"] >= 1
    assert s["private_pages"] >= 1


def test_archive_unarchive(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]

    ra = client.api_post(base + f"{pid}/archive/")
    assert_status(ra, 200)
    assert isinstance(ra.json()["archived_at"], str)
    assert client.api_get(base + f"{pid}/").json()["archived_at"] is not None

    ru = client.api_delete(base + f"{pid}/archive/")
    assert_status(ru, 204)
    assert client.api_get(base + f"{pid}/").json()["archived_at"] is None


def test_destroy_requires_archive(client, workspace, project):
    base, r = _create(client, workspace, project)
    pid = r.json()["id"]

    # deleting an un-archived page is rejected.
    rd = client.api_delete(base + f"{pid}/")
    assert_status(rd, 400)
    assert rd.json()["error"] == "The page should be archived before deleting"

    # archive, then delete succeeds.
    assert_status(client.api_post(base + f"{pid}/archive/"), 200)
    assert_status(client.api_delete(base + f"{pid}/"), 204)
