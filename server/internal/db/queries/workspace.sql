-- name: CreateWorkspace :one
insert into workspaces (name, slug, owner_id, organization_size, created_by)
values ($1, $2, $3, $4, $5)
returning *;

-- name: AddWorkspaceMember :exec
insert into workspace_members (workspace_id, member_id, role)
values ($1, $2, $3)
on conflict (workspace_id, member_id) do nothing;

-- name: GetWorkspaceBySlug :one
select * from workspaces where slug = $1 and deleted_at is null;

-- name: WorkspaceSlugExists :one
select exists(select 1 from workspaces where slug = $1);

-- name: WorkspaceMemberCount :one
select count(*) from workspace_members where workspace_id = $1;

-- name: GetWorkspaceMemberRole :one
select role from workspace_members where workspace_id = $1 and member_id = $2;

-- name: ListUserWorkspaces :many
select
    w.*,
    wm.role as member_role,
    (select count(*) from workspace_members m2 where m2.workspace_id = w.id) as total_members
from workspaces w
join workspace_members wm on wm.workspace_id = w.id and wm.member_id = $1
where w.deleted_at is null
order by w.created_at;

-- name: UpdateWorkspace :one
update workspaces set
    name              = $2,
    organization_size = $3,
    timezone          = $4,
    background_color  = $5,
    updated_by        = $6,
    updated_at        = now()
where slug = $1 and deleted_at is null
returning *;

-- name: UserFallbackWorkspace :one
select w.* from workspaces w
join workspace_members wm on wm.workspace_id = w.id and wm.member_id = $1
where w.deleted_at is null
order by w.created_at
limit 1;

-- name: CountPendingInvites :one
select count(*) from workspace_member_invites where email = $1 and accepted = false;

-- name: GetWorkspaceMemberByUser :one
select * from workspace_members where workspace_id = $1 and member_id = $2;

-- name: ListWorkspaceMembersFull :many
select wm.*, u.display_name, u.first_name, u.last_name, u.email, u.avatar, u.is_bot
from workspace_members wm
join users u on u.id = wm.member_id
where wm.workspace_id = $1
order by wm.created_at;

-- name: ProjectRolesForUser :many
select project_id, role from project_members where workspace_id = $1 and member_id = $2;
