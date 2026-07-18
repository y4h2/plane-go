-- +goose Up
-- +goose StatementBegin
create table estimates (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    name         text        not null,
    type         text        not null default 'points',
    description  text        not null default '',
    last_used    boolean     not null default false,
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);

create table estimate_points (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    estimate_id  uuid        not null references estimates(id) on delete cascade,
    key          integer     not null default 0,
    value        text        not null default '',
    description  text        not null default '',
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index estimate_points_estimate_idx on estimate_points (estimate_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists estimate_points;
drop table if exists estimates;
-- +goose StatementEnd
