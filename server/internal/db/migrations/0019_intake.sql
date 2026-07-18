-- +goose Up
-- +goose StatementBegin
create table intakes (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null default '',
    description  text        not null default '',
    is_default   boolean     not null default false,
    view_props   jsonb       not null default '{}',
    logo_props   jsonb       not null default '{}',
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (project_id, name)
);
create index intakes_project_idx on intakes (project_id);

create table intake_issues (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    intake_id    uuid        not null references intakes(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    status       integer     not null default -2,   -- -2 pending, -1 rejected, 0 snoozed, 1 accepted, 2 duplicate
    snoozed_till timestamptz,
    duplicate_to uuid        references issues(id) on delete set null,
    source       text        default 'IN_APP',
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index intake_issues_intake_idx on intake_issues (intake_id);
create index intake_issues_issue_idx on intake_issues (issue_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists intake_issues;
drop table if exists intakes;
-- +goose StatementEnd
