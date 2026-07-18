-- name: CreateWorkspaceInvite :one
insert into workspace_member_invites (workspace_id, email, role)
values ($1, $2, $3)
returning *;

-- name: ListInvitesForEmail :many
select wmi.*, w.slug as workspace_slug, w.name as workspace_name
from workspace_member_invites wmi
join workspaces w on w.id = wmi.workspace_id
where wmi.email = $1 and wmi.accepted = false
order by wmi.created_at;

-- name: GetInvite :one
select * from workspace_member_invites where id = $1;

-- name: AcceptInvite :exec
update workspace_member_invites set accepted = true where id = $1;
