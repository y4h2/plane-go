-- +goose Up
-- +goose StatementBegin
create table webhooks (
    id            uuid primary key default uuid_generate_v4(),
    workspace_id  uuid        not null references workspaces(id) on delete cascade,
    url           text        not null,
    is_active     boolean     not null default true,
    secret_key    text        not null,
    project       boolean     not null default false,
    issue         boolean     not null default false,
    module        boolean     not null default false,
    cycle         boolean     not null default false,
    issue_comment boolean     not null default false,
    is_internal   boolean     not null default false,
    version       text        not null default 'v1',
    created_by    uuid        references users(id),
    updated_by    uuid        references users(id),
    deleted_at    timestamptz,
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);
create unique index webhooks_workspace_url_unique_when_deleted_at_null
    on webhooks (workspace_id, url)
    where deleted_at is null;
create index webhooks_workspace_idx on webhooks (workspace_id);

-- webhook_logs: written by the async delivery worker (not implemented here),
-- so this table only backs the read-only /webhook-logs/ listing endpoint and
-- will typically be empty. Modeled after Django's WebhookLog for parity.
create table webhook_logs (
    id               uuid primary key default uuid_generate_v4(),
    workspace_id     uuid        not null references workspaces(id) on delete cascade,
    webhook          uuid        not null,
    event_type       text,
    request_method   text,
    request_headers  text,
    request_body     text,
    response_status  text,
    response_headers text,
    response_body    text,
    retry_count      smallint    not null default 0,
    created_by       uuid        references users(id),
    updated_by       uuid        references users(id),
    deleted_at       timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);
create index webhook_logs_workspace_webhook_idx on webhook_logs (workspace_id, webhook);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists webhook_logs;
drop table if exists webhooks;
-- +goose StatementEnd
