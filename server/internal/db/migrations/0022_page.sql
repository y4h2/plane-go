-- +goose Up
-- +goose StatementBegin
create table pages (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    name             text        not null default '',
    access           smallint    not null default 0,       -- 0 public, 1 private
    color            text        not null default '',
    description_html text        not null default '<p></p>',
    owned_by         uuid        not null references users(id) on delete cascade,
    parent_id        uuid        references pages(id) on delete cascade,
    archived_at      date,
    is_locked        boolean     not null default false,
    view_props       jsonb       not null default '{"full_width": false}',
    logo_props       jsonb       not null default '{}',
    created_by       uuid        references users(id),
    updated_by       uuid        references users(id),
    deleted_at       timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);
create index pages_workspace_idx on pages (workspace_id);
create index pages_parent_idx on pages (parent_id);

create table project_pages (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    page_id      uuid        not null references pages(id) on delete cascade,
    created_by   uuid        references users(id),
    updated_by   uuid        references users(id),
    deleted_at   timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index project_pages_page_idx on project_pages (page_id);
create index project_pages_project_idx on project_pages (project_id);
create unique index project_pages_uniq on project_pages (project_id, page_id) where deleted_at is null;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists project_pages;
drop table if exists pages;
-- +goose StatementEnd
