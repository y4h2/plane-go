-- name: CreateProject :one
insert into projects (workspace_id, name, identifier, description, created_by)
values ($1, $2, $3, $4, $5)
returning *;

-- name: GetProjectByID :one
select * from projects where id = $1 and workspace_id = $2 and deleted_at is null;

-- name: ListProjects :many
select p.*, coalesce(pm.role, 0)::smallint as member_role
from projects p
left join project_members pm on pm.project_id = p.id and pm.member_id = $2
where p.workspace_id = $1 and p.deleted_at is null
order by p.sort_order, p.created_at;

-- name: UpdateProject :one
update projects set
    name        = $3,
    description = $4,
    updated_by  = $5,
    updated_at  = now()
where id = $1 and workspace_id = $2 and deleted_at is null
returning *;

-- name: SoftDeleteProject :exec
update projects set deleted_at = now() where id = $1 and workspace_id = $2;

-- name: ProjectIdentifierExists :one
select exists(select 1 from projects where workspace_id = $1 and identifier = $2 and deleted_at is null);

-- name: ListProjectIdentifiers :many
select id, name, identifier from projects
where workspace_id = $1 and upper(identifier) = upper($2) and deleted_at is null;

-- name: ArchiveProject :exec
update projects set archived_at = now(), updated_at = now() where id = $1 and workspace_id = $2;

-- name: UnarchiveProject :exec
update projects set archived_at = null, updated_at = now() where id = $1 and workspace_id = $2;
