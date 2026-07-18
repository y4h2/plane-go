"""Structural assertion helpers.

Contract tests pin *shape*, not volatile values: they check status codes, the
pagination-envelope structure, and that each documented field is present with the
right type (ids/timestamps are type-checked, not value-matched). This keeps the
suite stable across runs and servers while still catching contract drift.
"""

import uuid


# Sentinels for field specs. A spec maps field name -> one of:
#   a python type / tuple of types  -> value must be an instance (None disallowed)
#   OPTIONAL(type)                  -> field must be present; value is type or None
#   ANY                             -> field must be present; any value incl. None
class _Any:
    def __repr__(self):  # pragma: no cover
        return "ANY"


ANY = _Any()


class OPTIONAL:
    """Field must be present; value is `types` or None."""

    def __init__(self, *types):
        self.types = types


def is_uuid(v) -> bool:
    if not isinstance(v, str):
        return False
    try:
        uuid.UUID(v)
        return True
    except (ValueError, AttributeError, TypeError):
        return False


def assert_status(resp, *allowed):
    assert resp.status_code in allowed, (
        f"{resp.request.method} {resp.request.url} -> {resp.status_code} "
        f"(want {allowed}): {resp.text[:400]}"
    )


def assert_fields(obj: dict, spec: dict, *, where: str = ""):
    """Assert obj has exactly the fields in spec, each matching its type spec.

    Extra keys are reported (contract drift in the other direction). Use
    `assert_has_fields` if you only want to assert a subset.
    """
    assert isinstance(obj, dict), f"{where}: expected dict, got {type(obj).__name__}"
    _check_fields(obj, spec, where, exact=True)


def assert_has_fields(obj: dict, spec: dict, *, where: str = ""):
    """Like assert_fields but tolerant of additional keys not in spec."""
    assert isinstance(obj, dict), f"{where}: expected dict, got {type(obj).__name__}"
    _check_fields(obj, spec, where, exact=False)


def _check_fields(obj, spec, where, exact):
    missing = [k for k in spec if k not in obj]
    assert not missing, f"{where}: missing fields {missing}; present={sorted(obj)}"
    if exact:
        extra = [k for k in obj if k not in spec]
        assert not extra, f"{where}: unexpected fields {extra}"
    for key, expected in spec.items():
        val = obj[key]
        _check_value(val, expected, f"{where}.{key}")


def _check_value(val, expected, where):
    if expected is ANY:
        return
    if isinstance(expected, OPTIONAL):
        if val is None:
            return
        assert isinstance(val, expected.types), (
            f"{where}: want {[t.__name__ for t in expected.types]} or None, got {type(val).__name__} ({val!r})"
        )
        return
    # bool is a subtype of int in python; keep them distinct for wire clarity.
    if expected is int:
        assert isinstance(val, int) and not isinstance(val, bool), f"{where}: want int, got {val!r}"
        return
    assert isinstance(val, expected), (
        f"{where}: want {getattr(expected, '__name__', expected)}, got {type(val).__name__} ({val!r})"
    )


# Cursor-paginated envelope used by the issue-family list endpoints.
ENVELOPE_KEYS = {
    "grouped_by": ANY,
    "sub_grouped_by": ANY,
    "total_count": int,
    "next_cursor": ANY,
    "prev_cursor": ANY,
    "next_page_results": bool,
    "prev_page_results": bool,
    "count": int,
    "total_pages": int,
    "total_results": int,
    "extra_stats": ANY,
    "results": ANY,  # list when ungrouped, dict when ?group_by=
}


def assert_envelope(obj: dict, *, where: str = "envelope", grouped: bool = False):
    """Assert the cursor-pagination envelope shape (issue-family lists)."""
    assert_has_fields(obj, ENVELOPE_KEYS, where=where)
    if grouped:
        assert isinstance(obj["results"], dict), f"{where}.results: want dict (grouped)"
    else:
        assert isinstance(obj["results"], list), f"{where}.results: want list"
