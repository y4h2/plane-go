-- name: GetUserProps :one
select * from project_user_properties where project_id = $1 and user_id = $2;

-- name: CreateUserProps :one
insert into project_user_properties (workspace_id, project_id, user_id)
values ($1, $2, $3)
on conflict (project_id, user_id) do update set updated_at = now()
returning *;

-- name: UpdateUserProps :one
update project_user_properties set filters = $3, display_filters = $4, display_properties = $5, updated_at = now()
where project_id = $1 and user_id = $2
returning *;
