-- +goose Up
-- +goose StatementBegin
create extension if not exists "uuid-ossp";

create table users (
    id            uuid primary key default uuid_generate_v4(),
    email         text        not null unique,
    password      text        not null,               -- Django-format pbkdf2 hash
    first_name    text        not null default '',
    last_name     text        not null default '',
    display_name  text        not null default '',
    avatar        text        not null default '',
    is_active     boolean     not null default true,
    is_bot        boolean     not null default false,
    date_joined   timestamptz not null default now(),
    last_login    timestamptz,
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);

create table sessions (
    key         text        primary key,              -- opaque 128-char token (the session-id cookie)
    user_id     uuid        not null references users(id) on delete cascade,
    created_at  timestamptz not null default now(),
    expires_at  timestamptz not null
);
create index sessions_user_id_idx on sessions (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists sessions;
drop table if exists users;
-- +goose StatementEnd
