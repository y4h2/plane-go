-- name: Subscribe :exec
insert into issue_subscribers (workspace_id, project_id, issue_id, subscriber_id)
values ($1, $2, $3, $4)
on conflict (issue_id, subscriber_id) do nothing;

-- name: Unsubscribe :exec
delete from issue_subscribers where issue_id = $1 and subscriber_id = $2;

-- name: IsSubscribed :one
select exists(select 1 from issue_subscribers where issue_id = $1 and subscriber_id = $2);

-- name: ListSubscribers :many
select s.id, s.subscriber_id, u.display_name, u.first_name, u.last_name, u.avatar, u.is_bot, u.email
from issue_subscribers s
join users u on u.id = s.subscriber_id
where s.issue_id = $1
order by s.created_at;

-- name: CreateReaction :one
insert into issue_reactions (workspace_id, project_id, issue_id, actor_id, reaction)
values ($1, $2, $3, $4, $5)
returning *;

-- name: ListReactions :many
select * from issue_reactions where issue_id = $1 order by created_at;
