-- +goose Up
-- +goose StatementBegin
create table issue_activities (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    actor_id     uuid        references users(id) on delete set null,
    verb         varchar(255) not null default 'created',
    field        varchar(255),
    old_value    text,
    new_value    text,
    comment      text        not null default '',
    old_identifier uuid,
    new_identifier uuid,
    epoch        double precision,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index issue_activities_issue_idx on issue_activities(issue_id, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists issue_activities;
-- +goose StatementEnd
