-- +goose Up
-- +goose StatementBegin
create table projects (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    name         text        not null,
    identifier   text        not null,
    description  text        not null default '',
    network      smallint    not null default 2,   -- 0=secret, 2=public-to-workspace
    sort_order   double precision not null default 65535,
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (workspace_id, identifier)
);

create table project_members (
    id           uuid primary key default uuid_generate_v4(),
    project_id   uuid        not null references projects(id) on delete cascade,
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    member_id    uuid        not null references users(id) on delete cascade,
    role         smallint    not null default 20,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (project_id, member_id)
);
create index project_members_project_idx on project_members (project_id);

create table workspace_member_invites (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    email        text        not null,
    role         smallint    not null default 15,
    accepted     boolean     not null default false,
    created_at   timestamptz not null default now()
);
create index wmi_email_idx on workspace_member_invites (email);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists workspace_member_invites;
drop table if exists project_members;
drop table if exists projects;
-- +goose StatementEnd
