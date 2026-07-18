-- +goose Up
-- +goose StatementBegin
create table states (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null,
    color        text        not null default '',
    group_name   text        not null default 'backlog',   -- exposed as "group"
    is_default   boolean     not null default false,        -- exposed as "default"
    description  text        not null default '',
    sequence     double precision not null default 65535,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (project_id, name)
);
create index states_project_idx on states (project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists states;
-- +goose StatementEnd
