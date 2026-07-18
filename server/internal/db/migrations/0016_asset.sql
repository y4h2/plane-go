-- +goose Up
-- +goose StatementBegin
create table assets (
    id                uuid primary key default uuid_generate_v4(),
    workspace_id      uuid,
    project_id        uuid,
    user_id           uuid,
    name              text        not null default '',
    content_type      text        not null default 'application/octet-stream',
    size              bigint      not null default 0,
    entity_type       text        not null default '',
    entity_identifier text        not null default '',
    is_uploaded       boolean     not null default false,
    created_at        timestamptz not null default now()
);

-- projects can reference an uploaded cover asset
alter table projects add column cover_image_asset uuid;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table projects drop column cover_image_asset;
drop table if exists assets;
-- +goose StatementEnd
