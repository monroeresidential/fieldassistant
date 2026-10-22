-- name: UpsertUserByProcoreID :one
insert into users (procore_user_id, email, name)
values ($1, $2, $3)
on conflict (procore_user_id) do update
set email = excluded.email, name = excluded.name
returning *;

-- name: GetUser :one
select * from users where id = $1;

-- name: SetReauthRequired :exec
update users set reauth_required = $2 where id = $1;

-- name: SetUserSelection :exec
update users set last_company_id = $2, last_project_id = $3 where id = $1;
