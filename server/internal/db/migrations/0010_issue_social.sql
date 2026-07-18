-- +goose Up
-- +goose StatementBegin
create table issue_subscribers (
    id            uuid primary key default uuid_generate_v4(),
    workspace_id  uuid        not null references workspaces(id) on delete cascade,
    project_id    uuid        not null references projects(id) on delete cascade,
    issue_id      uuid        not null references issues(id) on delete cascade,
    subscriber_id uuid        not null references users(id) on delete cascade,
    created_at    timestamptz not null default now(),
    unique (issue_id, subscriber_id)
);

create table issue_reactions (
    id           uuid primary key default uuid_generate_v4(),
    workspace_id uuid        not null references workspaces(id) on delete cascade,
    project_id   uuid        not null references projects(id) on delete cascade,
    issue_id     uuid        not null references issues(id) on delete cascade,
    actor_id     uuid        references users(id),
    reaction     text        not null,
    created_at   timestamptz not null default now(),
    unique (issue_id, actor_id, reaction)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists issue_reactions;
drop table if exists issue_subscribers;
-- +goose StatementEnd
