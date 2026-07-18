-- name: CreateComment :one
insert into issue_comments (workspace_id, project_id, issue_id, actor_id, comment_html, comment_stripped)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: ListComments :many
select * from issue_comments where issue_id = $1 order by created_at;

-- name: DeleteComment :exec
delete from issue_comments where id = $1 and issue_id = $2;

-- name: CreateLink :one
insert into issue_links (workspace_id, project_id, issue_id, url, title, created_by)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: ListLinks :many
select * from issue_links where issue_id = $1 order by created_at;

-- name: DeleteLink :exec
delete from issue_links where id = $1 and issue_id = $2;
