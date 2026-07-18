-- +goose Up
-- +goose StatementBegin
alter table users add column profile jsonb not null default '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table users drop column profile;
-- +goose StatementEnd
