-- name: CreateFavorite :one
insert into user_favorites (workspace_id, user_id, entity_type, entity_identifier, name, project_id)
values ($1, $2, $3, $4, $5, $6)
returning *;

-- name: ListFavorites :many
select * from user_favorites where workspace_id = $1 and user_id = $2 order by sequence, created_at;

-- name: DeleteFavorite :exec
delete from user_favorites where id = $1 and user_id = $2;

-- name: DeleteFavoriteByEntity :exec
delete from user_favorites where user_id = $1 and entity_identifier = $2;
