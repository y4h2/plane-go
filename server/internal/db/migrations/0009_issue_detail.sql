-- +goose Up
-- +goose StatementBegin
create table issue_comments (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    project_id       uuid        not null references projects(id) on delete cascade,
    issue_id         uuid        not null references issues(id) on delete cascade,
    actor_id         uuid        references users(id),
    comment_html     text        not null default '<p></p>',
    comment_stripped text        not null default '',
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);
create index issue_comments_issue_idx on issue_comments (issue_id);

create table issue_links (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    url          text        not null,
    title        text        not null default '',
    created_by   uuid        references users(id),
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);
create index issue_links_issue_idx on issue_links (issue_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists issue_links;
drop table if exists issue_comments;
-- +goose StatementEnd
