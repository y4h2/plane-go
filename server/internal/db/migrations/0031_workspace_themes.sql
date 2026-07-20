-- +goose Up
-- +goose StatementBegin
create table workspace_themes (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    name         text        not null,
    actor_id     uuid        not null references users(id) on delete cascade,
    colors       jsonb       not null default '{}',
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create unique index workspace_themes_workspace_name_uidx
    on workspace_themes (workspace_id, name)
    where deleted_at is null;
create index workspace_themes_workspace_idx on workspace_themes (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists workspace_themes;
-- +goose StatementEnd
