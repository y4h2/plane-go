-- name: CreateLabel :one
insert into labels (workspace_id, project_id, name, color, sort_order)
values ($1, $2, $3, $4, $5)
returning *;

-- name: GetLabel :one
select * from labels where id = $1 and project_id = $2;

-- name: ListLabels :many
select * from labels where project_id = $1 order by sort_order, created_at;

-- name: ListWorkspaceLabels :many
select * from labels where workspace_id = $1 order by sort_order, created_at;

-- name: LabelNameExists :one
select exists(select 1 from labels where project_id = $1 and lower(name) = lower($2));

-- name: NextLabelSortOrder :one
select (coalesce(max(sort_order), 55535) + 10000)::double precision from labels where project_id = $1;

-- name: UpdateLabel :one
update labels set name = $3, color = $4, updated_at = now()
where id = $1 and project_id = $2
returning *;

-- name: DeleteLabel :exec
delete from labels where id = $1 and project_id = $2;
