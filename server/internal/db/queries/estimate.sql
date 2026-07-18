-- name: CreateEstimate :one
insert into estimates (workspace_id, project_id, name, type, description, created_by)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: CreateEstimatePoint :one
insert into estimate_points (workspace_id, project_id, estimate_id, key, value, created_by)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: ListEstimates :many
select * from estimates where project_id = $1 and deleted_at is null order by created_at;

-- name: GetEstimate :one
select * from estimates where id = $1 and project_id = $2 and deleted_at is null;

-- name: ListEstimatePoints :many
select * from estimate_points where estimate_id = $1 and deleted_at is null order by key;

-- name: UpdateEstimate :one
update estimates set name = $2, type = $3, updated_at = now() where id = $1 and project_id = $4 returning *;

-- name: DeleteEstimate :exec
update estimates set deleted_at = now() where id = $1 and project_id = $2;

-- name: UpdateEstimatePointValue :exec
update estimate_points set value = $2, updated_at = now() where id = $1;
