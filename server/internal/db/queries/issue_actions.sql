-- name: SetIssueParent :exec
update issues set parent_id = $3, updated_at = now() where id = $1 and project_id = $2;

-- name: ListSubIssues :many
select * from issues where parent_id = $1 and deleted_at is null order by sort_order, created_at;

-- name: ArchiveIssue :exec
update issues set archived_at = now(), updated_at = now() where id = $1 and project_id = $2;

-- name: UnarchiveIssue :exec
update issues set archived_at = null, updated_at = now() where id = $1 and project_id = $2;

-- name: StateGroupForIssue :one
select coalesce(s.group_name, 'backlog')::text
from issues i
left join states s on s.id = i.state_id
where i.id = $1;

-- name: BulkSoftDeleteIssues :exec
update issues set deleted_at = now() where project_id = $1 and id = any($2::uuid[]);

-- name: CreateRelation :exec
insert into issue_relations (workspace_id, project_id, issue_id, related_issue_id, relation_type)
values ($1, $2, $3, $4, $5)
on conflict (issue_id, related_issue_id, relation_type) do nothing;

-- name: ListRelations :many
select relation_type, related_issue_id from issue_relations where issue_id = $1;

-- name: DeleteRelation :exec
delete from issue_relations where issue_id = $1 and related_issue_id = $2 and relation_type = $3;

-- name: ListIssueAttachments :many
select * from assets where entity_type = 'ISSUE_ATTACHMENT' and entity_identifier = $1 and is_uploaded = true order by created_at;
