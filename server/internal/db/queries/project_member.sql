-- name: AddProjectMember :one
insert into project_members (project_id, workspace_id, member_id, role)
values ($1, $2, $3, $4)
on conflict (project_id, member_id) do update set role = excluded.role
returning *;

-- name: ListProjectMembers :many
select * from project_members where project_id = $1 order by created_at;

-- name: GetProjectMember :one
select * from project_members where project_id = $1 and id = $2;

-- name: GetProjectMemberByUser :one
select * from project_members where project_id = $1 and member_id = $2;

-- name: ProjectMemberUserIDs :many
select member_id from project_members where project_id = $1 order by created_at;

-- name: UpdateProjectMemberRole :one
update project_members set role = $3, updated_at = now()
where project_id = $1 and id = $2
returning *;
