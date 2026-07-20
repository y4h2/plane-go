-- +goose Up
-- +goose StatementBegin
create table user_recent_visits (
    id                uuid primary key default uuid_generate_v4(),
    workspace_id      uuid        not null references workspaces(id) on delete cascade,
    user_id           uuid        not null references users(id) on delete cascade,
    project_id        uuid        references projects(id) on delete cascade,
    entity_name       varchar(255) not null,
    entity_identifier uuid        not null,
    visited_at        timestamptz not null default now(),
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    unique (user_id, workspace_id, entity_name, entity_identifier)
);
create index user_recent_visits_idx on user_recent_visits(user_id, workspace_id, visited_at desc);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists user_recent_visits;
-- +goose StatementEnd
