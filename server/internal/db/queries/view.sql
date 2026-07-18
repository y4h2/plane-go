-- name: CreateView :one
insert into views (workspace_id, project_id, name, description, access, query, filters, display_filters, display_properties, owned_by, created_by)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
returning *;

-- name: GetView :one
select * from views where id = $1 and workspace_id = $2 and deleted_at is null;

-- name: ListWorkspaceViews :many
select * from views where workspace_id = $1 and project_id is null and deleted_at is null order by created_at;

-- name: ListProjectViews :many
select * from views where project_id = $1 and deleted_at is null order by created_at;

-- name: UpdateView :one
update views set name = $3, description = $4, access = $5, filters = $6, query = $7,
    display_filters = $8, display_properties = $9, updated_by = $10, updated_at = now()
where id = $1 and workspace_id = $2 and deleted_at is null
returning *;

-- name: SoftDeleteView :exec
update views set deleted_at = now() where id = $1 and workspace_id = $2;
