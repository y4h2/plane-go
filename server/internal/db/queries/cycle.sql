-- name: CreateCycle :one
insert into cycles (workspace_id, project_id, name, description, start_date, end_date, owned_by_id, created_by)
values ($1, $2, $3, $4, $5, $6, $7, $8)
returning *;

-- name: GetCycle :one
select * from cycles where id = $1 and project_id = $2 and deleted_at is null;

-- name: ListCycles :many
select * from cycles where project_id = $1 and deleted_at is null order by created_at;

-- name: ListWorkspaceCycles :many
select * from cycles where workspace_id = $1 and deleted_at is null order by created_at;

-- name: UpdateCycle :one
update cycles set name = $3, description = $4, start_date = $5, end_date = $6, updated_at = now()
where id = $1 and project_id = $2 and deleted_at is null
returning *;

-- name: SoftDeleteCycle :exec
update cycles set deleted_at = now() where id = $1 and project_id = $2;

-- name: CountOverlappingCycles :one
select count(*) from cycles
where project_id = sqlc.arg(project_id) and deleted_at is null
  and start_date is not null and end_date is not null
  and start_date <= sqlc.arg(range_end) and end_date >= sqlc.arg(range_start);

-- name: AddCycleIssue :exec
insert into cycle_issues (workspace_id, project_id, cycle_id, issue_id)
values ($1, $2, $3, $4)
on conflict (cycle_id, issue_id) do nothing;

-- name: ListCycleIssueIssues :many
select i.* from issues i
join cycle_issues ci on ci.issue_id = i.id
where ci.cycle_id = $1 and i.deleted_at is null
order by i.sort_order, i.created_at;

-- name: TransferCycleIssues :exec
update cycle_issues set cycle_id = $2 where cycle_id = $1;
