-- +goose Up
-- +goose StatementBegin
create table analytic_views (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    name         text        not null,
    description  text        not null default '',
    query        jsonb       not null default '{}',
    query_dict   jsonb       not null default '{}',
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index analytic_views_workspace_idx on analytic_views (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists analytic_views;
-- +goose StatementEnd
