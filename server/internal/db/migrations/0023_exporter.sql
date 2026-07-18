-- +goose Up
-- +goose StatementBegin
create table exporters (
    id              uuid primary key default uuid_generate_v4(),
    name            text,
    type            text        not null default 'issue_exports',
    workspace_id    uuid        not null references workspaces(id) on delete cascade,
    project         uuid[]      not null default '{}',
    provider        text        not null,
    status          text        not null default 'queued',
    reason          text        not null default '',
    key             text        not null default '',
    url             text,
    token           text        not null unique,
    initiated_by_id uuid        not null references users(id) on delete cascade,
    filters         jsonb,
    rich_filters    jsonb       default '{}',
    created_by      uuid        references users(id),
    updated_by      uuid        references users(id),
    deleted_at      timestamptz,
    created_at      timestamptz not null default now(),
    updated_at      timestamptz not null default now()
);
create index exporters_workspace_idx on exporters (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists exporters;
-- +goose StatementEnd
