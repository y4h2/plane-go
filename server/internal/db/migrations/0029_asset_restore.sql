-- +goose Up
-- +goose StatementBegin
-- Soft-delete marker for assets, mirroring FileAsset.deleted_at on the Python
-- side. Nothing else reads this column yet; it exists so DELETE can mark an
-- asset deleted and POST .../restore/... can clear it again.
alter table assets add column deleted_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table assets drop column deleted_at;
-- +goose StatementEnd
