-- +goose Up
-- +goose StatementBegin
create table labels (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null,
    color        text        not null default '',
    parent_id    uuid,
    sort_order   double precision not null default 65535,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (project_id, name)
);
create index labels_project_idx on labels (project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists labels;
-- +goose StatementEnd
