-- +goose Up
-- +goose StatementBegin
create table cycles (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null,
    description  text        not null default '',
    start_date   timestamptz,
    end_date     timestamptz,
    owned_by_id  uuid        not null references users(id),
    sort_order   double precision not null default 65535,
    created_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index cycles_project_idx on cycles (project_id);

create table cycle_issues (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    cycle_id     uuid        not null references cycles(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    created_at   timestamptz not null default now(),
    unique (cycle_id, issue_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists cycle_issues;
drop table if exists cycles;
-- +goose StatementEnd
