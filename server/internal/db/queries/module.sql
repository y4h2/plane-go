-- name: CreateModule :one
insert into modules (workspace_id, project_id, name, description, status, created_by)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: GetModule :one
select * from modules where id = $1 and project_id = $2 and deleted_at is null and archived_at is null;

-- name: ListModules :many
select * from modules where project_id = $1 and deleted_at is null and archived_at is null order by created_at;

-- name: ListWorkspaceModules :many
select * from modules where workspace_id = $1 and deleted_at is null order by created_at;

-- name: ModuleNameExists :one
select exists(select 1 from modules where project_id = $1 and lower(name) = lower($2) and deleted_at is null);

-- name: UpdateModule :one
update modules set name = $3, description = $4, status = $5, updated_by = $6, updated_at = now()
where id = $1 and project_id = $2 and deleted_at is null
returning *;

-- name: SoftDeleteModule :exec
update modules set deleted_at = now() where id = $1 and project_id = $2;

-- name: AddModuleIssue :exec
insert into module_issues (workspace_id, project_id, module_id, issue_id)
values ($1, $2, $3, $4)
on conflict (module_id, issue_id) do nothing;

-- name: RemoveModuleIssue :exec
delete from module_issues where module_id = $1 and issue_id = $2;

-- name: ListModuleIssueIssues :many
select i.* from issues i
join module_issues mi on mi.issue_id = i.id
where mi.module_id = $1 and i.deleted_at is null
order by i.sort_order, i.created_at;
