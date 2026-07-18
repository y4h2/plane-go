-- +goose Up
-- +goose StatementBegin
create table issue_relations (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    project_id       uuid        not null references projects(id) on delete cascade,
    issue_id         uuid        not null references issues(id) on delete cascade,
    related_issue_id uuid        not null references issues(id) on delete cascade,
    relation_type    text        not null,
    created_at       timestamptz not null default now(),
    unique (issue_id, related_issue_id, relation_type)
);
create index issue_relations_issue_idx on issue_relations (issue_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists issue_relations;
-- +goose StatementEnd
