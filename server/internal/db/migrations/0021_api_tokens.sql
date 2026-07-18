-- +goose Up
-- +goose StatementBegin
create table api_tokens (
    id                 uuid primary key default uuid_generate_v4(),
    label              text        not null default '',
    description        text        not null default '',
    is_active          boolean     not null default true,
    last_used          timestamptz,
    token              text        not null unique,
    user_id            uuid        not null references users(id) on delete cascade,
    user_type          smallint    not null default 0,
    workspace_id       uuid        references workspaces(id) on delete cascade,
    expired_at         timestamptz,
    is_service         boolean     not null default false,
    allowed_rate_limit text        not null default '60/min',
    created_by         uuid        references users(id),
    updated_by         uuid        references users(id),
    deleted_at         timestamptz,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);
create index api_tokens_user_idx on api_tokens (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists api_tokens;
-- +goose StatementEnd
