-- name: CreateState :one
insert into states (workspace_id, project_id, name, color, group_name, is_default, description, sequence)
values ($1, $2, $3, $4, $5, $6, $7, $8)
returning *;

-- name: GetState :one
select * from states where id = $1 and project_id = $2;

-- name: ListStates :many
select * from states where project_id = $1 order by sequence, created_at;

-- name: ListWorkspaceStates :many
select * from states where workspace_id = $1 order by sequence, created_at;

-- name: StateNameExists :one
select exists(select 1 from states where project_id = $1 and lower(name) = lower($2));

-- name: UpdateState :one
update states set name = $3, color = $4, description = $5, updated_at = now()
where id = $1 and project_id = $2
returning *;

-- name: ClearDefaultStates :exec
update states set is_default = false where project_id = $1;

-- name: SetDefaultState :exec
update states set is_default = true where id = $1 and project_id = $2;

-- name: DeleteState :exec
delete from states where id = $1 and project_id = $2;
