-- +goose Up
-- +goose StatementBegin
-- The Python reference's UserFavorite model carries
--   UniqueConstraint(fields=["entity_type", "entity_identifier", "user"],
--                     condition=Q(deleted_at__isnull=True))
-- (apps/api/plane/db/models/favorite.py). 0014_favorites.sql created
-- user_favorites without that constraint. This table has no deleted_at
-- column (favorites are hard-deleted everywhere in this port -- see
-- internal/page and internal/wsextra), so a plain unique index is the exact
-- equivalent: a favorited (entity_type, entity_identifier) can only be
-- favorited once per user, and deleting it frees the slot immediately.
--
-- Needed so POSTing the same view favorite twice 400s (unique_violation)
-- exactly like the reference, instead of silently inserting a duplicate row.
create unique index if not exists user_favorites_entity_user_idx
    on user_favorites (entity_type, entity_identifier, user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop index if exists user_favorites_entity_user_idx;
-- +goose StatementEnd
