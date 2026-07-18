-- +goose Up
-- +goose StatementBegin
create table issues (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    project_id       uuid        not null references projects(id) on delete cascade,
    name             text        not null,
    description_html text        not null default '<p></p>',
    priority         text        not null default 'none',
    state_id         uuid,
    parent_id        uuid,
    estimate_point   uuid,
    sequence_id      integer     not null default 1,
    sort_order       double precision not null default 65535,
    start_date       date,
    target_date      date,
    completed_at     timestamptz,
    is_draft         boolean     not null default false,
    archived_at      timestamptz,
    created_by       uuid        references users(id),
    updated_by       uuid        references users(id),
    deleted_at       timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);
create index issues_project_idx on issues (project_id);
create index issues_state_idx on issues (state_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists issues;
-- +goose StatementEnd
