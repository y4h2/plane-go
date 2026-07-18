-- +goose Up
-- +goose StatementBegin
create table workspaces (
    id                uuid primary key default uuid_generate_v4(),
    name              text        not null,
    slug              text        not null unique,
    owner_id          uuid        not null references users(id),
    logo              text,
    logo_asset        uuid,
    organization_size text,
    timezone          text        not null default 'UTC',
    background_color  text        not null default '#6c5ce7',
    created_by        uuid        references users(id),
    updated_by        uuid        references users(id),
    deleted_at        timestamptz,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now()
);

create table workspace_members (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    member_id    uuid        not null references users(id) on delete cascade,
    role         smallint    not null default 20,   -- GUEST=5, MEMBER=15, ADMIN=20
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (workspace_id, member_id)
);
create index workspace_members_ws_idx on workspace_members (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists workspace_members;
drop table if exists workspaces;
-- +goose StatementEnd
