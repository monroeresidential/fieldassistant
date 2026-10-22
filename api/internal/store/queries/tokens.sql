-- name: UpsertProcoreTokens :exec
insert into procore_tokens (user_id, refresh_token_enc, access_token_enc, expires_at)
values ($1, $2, $3, $4)
on conflict (user_id) do update
set refresh_token_enc = excluded.refresh_token_enc,
    access_token_enc = excluded.access_token_enc,
    expires_at = excluded.expires_at,
    updated_at = now();

-- name: GetProcoreTokens :one
select * from procore_tokens where user_id = $1;
