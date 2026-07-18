-- +goose Up
-- +goose StatementBegin
-- draft_issues mirrors many issues columns, but project_id and name are nullable
-- (a draft can exist before it is assigned to a project or given a title).
create table draft_issues (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    project_id       uuid        references projects(id) on delete cascade,
    name             text,
    description_html text        not null default '<p></p>',
    priority         text        not null default 'none',
    state_id         uuid,
    parent_id        uuid,
    estimate_point   uuid,
    type_id          uuid,
    sort_order       double precision not null default 65535,
    start_date       date,
    target_date      date,
    completed_at     timestamptz,
    created_by       uuid        references users(id),
    updated_by       uuid        references users(id),
    deleted_at       timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);
create index draft_issues_workspace_idx on draft_issues (workspace_id);
create index draft_issues_created_by_idx on draft_issues (created_by);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists draft_issues;
-- +goose StatementEnd
