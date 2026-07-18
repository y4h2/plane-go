-- +goose Up
-- +goose StatementBegin
alter table cycles add column archived_at timestamptz;
alter table modules add column archived_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table cycles drop column archived_at;
alter table modules drop column archived_at;
-- +goose StatementEnd
