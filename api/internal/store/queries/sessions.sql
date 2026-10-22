-- name: CreateSession :one
insert into sessions (user_id, device_id, refresh_hash, expires_at)
values ($1, $2, $3, $4)
returning *;

-- name: GetSession :one
select * from sessions where id = $1;

-- name: GetSessionByRefreshHash :one
select * from sessions where refresh_hash = $1;

-- name: GetSessionByPrevRefreshHash :one
select * from sessions where prev_refresh_hash = $1;

-- name: RotateSessionRefresh :one
update sessions
set refresh_hash = $2, prev_refresh_hash = refresh_hash, expires_at = $3
where id = $1 and refresh_hash = $4
returning id;

-- name: DeleteSession :exec
delete from sessions where id = $1;

-- name: DeleteSessionsForUser :exec
delete from sessions where user_id = $1;
