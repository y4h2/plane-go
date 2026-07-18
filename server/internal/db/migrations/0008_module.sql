-- +goose Up
-- +goose StatementBegin
create table modules (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null,
    description  text        not null default '',
    status       text        not null default 'backlog',
    lead_id      uuid,
    sort_order   double precision not null default 65535,
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (project_id, name)
);
create index modules_project_idx on modules (project_id);

create table module_issues (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    module_id    uuid        not null references modules(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    created_at   timestamptz not null default now(),
    unique (module_id, issue_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists module_issues;
drop table if exists modules;
-- +goose StatementEnd
