-- +goose Up
-- +goose StatementBegin
create table user_favorites (
    id                uuid primary key default uuid_generate_v4(),
    workspace_id      uuid        not null references workspaces(id) on delete cascade,
    user_id           uuid        not null references users(id) on delete cascade,
    entity_type       text        not null,
    entity_identifier uuid,
    name              text        not null default '',
    is_folder         boolean     not null default false,
    parent            uuid,
    project_id        uuid,
    sequence          double precision not null default 65535,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now()
);
create index user_favorites_ws_user_idx on user_favorites (workspace_id, user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists user_favorites;
-- +goose StatementEnd
