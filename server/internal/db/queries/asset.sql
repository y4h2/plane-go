-- name: CreateAsset :one
insert into assets (workspace_id, project_id, user_id, name, content_type, size, entity_type, entity_identifier)
values ($1, $2, $3, $4, $5, $6, $7, $8)
returning *;

-- name: GetAsset :one
select * from assets where id = $1;

-- name: MarkAssetUploaded :exec
update assets set is_uploaded = true where id = $1;

-- name: SetProjectCover :exec
update projects set cover_image_asset = $3, updated_at = now() where id = $1 and workspace_id = $2;
