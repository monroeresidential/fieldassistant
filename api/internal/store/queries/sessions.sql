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

-- name: RotateSessionRefresh :exec
update sessions
set refresh_hash = $2, prev_refresh_hash = $3, expires_at = $4
where id = $1;

-- name: DeleteSession :exec
delete from sessions where id = $1;

-- name: DeleteSessionsForUser :exec
delete from sessions where user_id = $1;
