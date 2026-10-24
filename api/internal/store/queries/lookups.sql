-- name: UpsertLookup :exec
insert into lookup_cache (company_id, project_id, kind, etag, body, unavailable, reason, fetched_at)
values ($1, $2, $3, $4, $5, $6, $7, now())
on conflict (company_id, project_id, kind) do update
set etag = excluded.etag,
    body = excluded.body,
    unavailable = excluded.unavailable,
    reason = excluded.reason,
    fetched_at = excluded.fetched_at;

-- name: GetLookup :one
select * from lookup_cache
where company_id = $1 and project_id = $2 and kind = $3;
