-- +goose Up
-- +goose StatementBegin
create table project_user_properties (
    id                 uuid primary key default uuid_generate_v4(),
    workspace_id       uuid        not null references workspaces(id) on delete cascade,
    project_id         uuid        not null references projects(id) on delete cascade,
    user_id            uuid        not null references users(id) on delete cascade,
    filters            jsonb       not null default '{}',
    display_filters    jsonb       not null default '{}',
    display_properties jsonb       not null default '{}',
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (project_id, user_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists project_user_properties;
-- +goose StatementEnd
