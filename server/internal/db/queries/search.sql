-- name: SearchIssues :many
select i.id, i.name, i.sequence_id, i.project_id, p.identifier as project_identifier, w.slug as workspace_slug
from issues i join projects p on p.id = i.project_id join workspaces w on w.id = i.workspace_id
where i.workspace_id = $1 and i.deleted_at is null and i.name ilike '%' || $2 || '%'
order by i.created_at desc limit 20;

-- name: SearchProjects :many
select p.id, p.name, p.identifier, w.slug as workspace_slug
from projects p join workspaces w on w.id = p.workspace_id
where p.workspace_id = $1 and p.deleted_at is null and p.name ilike '%' || $2 || '%'
order by p.created_at desc limit 20;

-- name: SearchCycles :many
select c.id, c.name, c.project_id, p.identifier as project_identifier, w.slug as workspace_slug
from cycles c join projects p on p.id = c.project_id join workspaces w on w.id = c.workspace_id
where c.workspace_id = $1 and c.deleted_at is null and c.name ilike '%' || $2 || '%'
order by c.created_at desc limit 20;

-- name: SearchModules :many
select m.id, m.name, m.project_id, p.identifier as project_identifier, w.slug as workspace_slug
from modules m join projects p on p.id = m.project_id join workspaces w on w.id = m.workspace_id
where m.workspace_id = $1 and m.deleted_at is null and m.name ilike '%' || $2 || '%'
order by m.created_at desc limit 20;

-- name: ProjectStats :many
select p.id,
    (select count(*) from issues i where i.project_id = p.id and i.deleted_at is null) as total_issues,
    (select count(*) from issues i join states s on s.id = i.state_id where i.project_id = p.id and i.deleted_at is null and s.group_name = 'completed') as completed_issues,
    (select count(*) from cycles c where c.project_id = p.id and c.deleted_at is null) as total_cycles,
    (select count(*) from modules m where m.project_id = p.id and m.deleted_at is null) as total_modules,
    (select count(*) from project_members pm where pm.project_id = p.id) as total_members
from projects p where p.workspace_id = $1 and p.deleted_at is null;
