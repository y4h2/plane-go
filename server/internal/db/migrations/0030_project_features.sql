-- +goose Up
-- +goose StatementBegin
alter table projects add column module_view boolean not null default false;
alter table projects add column cycle_view boolean not null default false;
alter table projects add column issue_views_view boolean not null default false;
alter table projects add column page_view boolean not null default true;
alter table projects add column intake_view boolean not null default false;
alter table projects add column is_time_tracking_enabled boolean not null default false;
alter table projects add column is_issue_type_enabled boolean not null default false;
alter table projects add column guest_view_all_features boolean not null default false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table projects drop column guest_view_all_features;
alter table projects drop column is_issue_type_enabled;
alter table projects drop column is_time_tracking_enabled;
alter table projects drop column intake_view;
alter table projects drop column page_view;
alter table projects drop column issue_views_view;
alter table projects drop column cycle_view;
alter table projects drop column module_view;
-- +goose StatementEnd
