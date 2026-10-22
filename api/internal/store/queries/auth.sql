-- name: CreateAuthState :exec
insert into auth_states (state, device_id, expires_at) values ($1, $2, $3);

-- name: ConsumeAuthState :one
delete from auth_states where state = $1 and expires_at > now() returning device_id;

-- name: CreateAuthCode :exec
insert into auth_codes (code, session_id, expires_at) values ($1, $2, $3);

-- name: ConsumeAuthCode :one
delete from auth_codes where code = $1 and expires_at > now() returning session_id;
