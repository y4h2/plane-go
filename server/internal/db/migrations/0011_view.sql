-- +goose Up
-- +goose StatementBegin
create table views (
    id                 uuid primary key default uuid_generate_v4(),
    workspace_id       uuid        not null references workspaces(id) on delete cascade,
    project_id         uuid        references projects(id) on delete cascade,  -- null for workspace-level views
    name               text        not null,
    description        text        not null default '',
    access             smallint    not null default 1,
    query              jsonb       not null default '{}',
    filters            jsonb       not null default '{}',
    display_filters    jsonb       not null default '{}',
    display_properties jsonb       not null default '{}',
    logo_props         jsonb       not null default '{}',
    sort_order         double precision not null default 65535,
    is_locked          boolean     not null default false,
    owned_by           uuid        references users(id),
    created_by         uuid        references users(id),
    updated_by         uuid        references users(id),
    deleted_at         timestamptz,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);
create index views_workspace_idx on views (workspace_id);
create index views_project_idx on views (project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists views;
-- +goose StatementEnd
