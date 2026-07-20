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

-- name: UpdateProjectFeatures :one
update projects set
    name                      = $3,
    description               = $4,
    module_view               = $5,
    cycle_view                = $6,
    issue_views_view          = $7,
    page_view                 = $8,
    intake_view               = $9,
    is_time_tracking_enabled  = $10,
    is_issue_type_enabled     = $11,
    guest_view_all_features   = $12,
    updated_by                = $13,
    updated_at                = now()
where id = $1 and workspace_id = $2 and deleted_at is null
returning *;

-- name: SearchProjectIssues :many
-- Backs the project-scoped issue search endpoint (search-issues/) in its
-- default (workspace_search=false) mode: results are scoped to one project.
-- $4 is the free-text query ('' means "no text filter", i.e. every issue in
-- scope). $5 is the set of numeric substrings pulled from the query text by the
-- caller (Django also OR-matches query digits against sequence_id); an empty
-- array is a no-op in the `= any(...)` check.
select
    i.id,
    i.name,
    i.start_date,
    i.sequence_id,
    i.project_id,
    p.name as project_name,
    p.identifier as project_identifier,
    w.slug as workspace_slug,
    coalesce(s.name, '') as state_name,
    coalesce(s.group_name, '') as state_group,
    coalesce(s.color, '') as state_color
from issues i
join projects p on p.id = i.project_id
join workspaces w on w.id = p.workspace_id
join project_members pm on pm.project_id = i.project_id and pm.member_id = $2
left join states s on s.id = i.state_id
where p.id = $3
  and w.slug = $1
  and i.deleted_at is null
  and p.deleted_at is null
  and p.archived_at is null
  and (
    $4::text = ''
    or i.name ilike '%' || $4 || '%'
    or p.identifier ilike '%' || $4 || '%'
    or i.sequence_id = any($5::int[])
  )
order by i.created_at desc
limit 100;

-- name: SearchWorkspaceIssues :many
-- Backs search-issues/ in workspace_search=true mode: results span every
-- project in the workspace the caller is a member of. Same param semantics as
-- SearchProjectIssues minus the project scoping.
select
    i.id,
    i.name,
    i.start_date,
    i.sequence_id,
    i.project_id,
    p.name as project_name,
    p.identifier as project_identifier,
    w.slug as workspace_slug,
    coalesce(s.name, '') as state_name,
    coalesce(s.group_name, '') as state_group,
    coalesce(s.color, '') as state_color
from issues i
join projects p on p.id = i.project_id
join workspaces w on w.id = p.workspace_id
join project_members pm on pm.project_id = i.project_id and pm.member_id = $2
left join states s on s.id = i.state_id
where w.slug = $1
  and i.deleted_at is null
  and p.deleted_at is null
  and p.archived_at is null
  and (
    $3::text = ''
    or i.name ilike '%' || $3 || '%'
    or p.identifier ilike '%' || $3 || '%'
    or i.sequence_id = any($4::int[])
  )
order by i.created_at desc
limit 100;
