-- +goose Up
-- +goose StatementBegin
alter table projects add column archived_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table projects drop column archived_at;
-- +goose StatementEnd
