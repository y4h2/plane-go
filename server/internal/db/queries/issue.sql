-- name: NextIssueSequence :one
select coalesce(max(sequence_id), 0) + 1 from issues where project_id = $1;

-- name: CreateIssue :one
insert into issues (workspace_id, project_id, name, priority, state_id, sequence_id, created_by)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: GetIssue :one
select * from issues where id = $1 and project_id = $2 and deleted_at is null;

-- name: ListIssues :many
select * from issues where project_id = $1 and deleted_at is null order by sort_order, created_at;

-- name: ListIssuesByIDs :many
select * from issues where project_id = $1 and id = any($2::uuid[]) and deleted_at is null;

-- name: UpdateIssue :exec
update issues set
    name       = $3,
    priority   = $4,
    state_id   = $5,
    updated_by = $6,
    updated_at = now()
where id = $1 and project_id = $2 and deleted_at is null;

-- name: SoftDeleteIssue :exec
update issues set deleted_at = now() where id = $1 and project_id = $2;

-- name: CountIssuesByState :one
select count(*) from issues where state_id = $1 and deleted_at is null;
