-- name: CreateSession :one
insert into sessions (key, user_id, expires_at)
values ($1, $2, $3)
returning *;

-- name: GetSessionUser :one
select u.*
from sessions s
join users u on u.id = s.user_id
where s.key = $1 and s.expires_at > now();

-- name: DeleteSession :exec
delete from sessions where key = $1;
