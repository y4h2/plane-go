-- name: CreateUser :one
insert into users (email, password, display_name)
values ($1, $2, $3)
returning *;

-- name: GetUserByEmail :one
select * from users where email = $1;

-- name: GetUserByID :one
select * from users where id = $1;

-- name: TouchLastLogin :exec
update users set last_login = now(), updated_at = now() where id = $1;

-- name: UpdateUserNames :one
update users set first_name = $2, last_name = $3, display_name = $4, updated_at = now()
where id = $1
returning *;

-- name: MergeUserProfile :one
update users set profile = profile || $2::jsonb, updated_at = now()
where id = $1
returning profile;
