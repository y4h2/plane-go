-- +goose Up
-- +goose StatementBegin
create table module_user_properties (
    id                 uuid primary key default uuid_generate_v4(),
    workspace_id       uuid        not null references workspaces(id) on delete cascade,
    project_id         uuid        not null references projects(id) on delete cascade,
    module_id          uuid        not null references modules(id) on delete cascade,
    user_id            uuid        not null references users(id) on delete cascade,
    filters            jsonb       not null default '{}',
    display_filters    jsonb       not null default '{}',
    display_properties jsonb       not null default '{}',
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (module_id, user_id)
);
create table cycle_user_properties (
    id                 uuid primary key default uuid_generate_v4(),
    workspace_id       uuid        not null references workspaces(id) on delete cascade,
    project_id         uuid        not null references projects(id) on delete cascade,
    cycle_id           uuid        not null references cycles(id) on delete cascade,
    user_id            uuid        not null references users(id) on delete cascade,
    filters            jsonb       not null default '{}',
    display_filters    jsonb       not null default '{}',
    display_properties jsonb       not null default '{}',
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (cycle_id, user_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists module_user_properties;
drop table if exists cycle_user_properties;
-- +goose StatementEnd
