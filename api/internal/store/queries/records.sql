-- name: UpsertRecord :one
-- change_seq moves only when the row is new, its body changed, or it was
-- soft-deleted (resurrection); an unchanged row only refreshes synced_at.
insert into user_records (
  company_id, user_id, project_id, kind, procore_id, procore_updated_at,
  body, body_hash, change_seq
) values (
  @company_id, @user_id, @project_id, @kind, @procore_id, @procore_updated_at,
  @body, @body_hash, nextval('records_change_seq')
)
on conflict (user_id, project_id, kind, procore_id) do update set
  procore_updated_at = excluded.procore_updated_at,
  body = excluded.body,
  body_hash = excluded.body_hash,
  change_seq = case
    when user_records.body_hash <> excluded.body_hash or user_records.deleted_at is not null
    then nextval('records_change_seq')
    else user_records.change_seq
  end,
  deleted_at = null,
  synced_at = now()
returning change_seq;

-- name: SoftDeleteMissingRecords :exec
-- Each row gets its own change_seq so the deletion reaches clients once.
update user_records
set deleted_at = @deleted_at, change_seq = nextval('records_change_seq')
where user_id = @user_id and project_id = @project_id and kind = @kind
  and deleted_at is null
  and procore_id <> all(@keep_ids::bigint[]);

-- name: ListRecordDelta :many
select * from user_records
where user_id = @user_id and project_id = @project_id and kind = @kind
  and change_seq > @since
order by change_seq
limit sqlc.arg('limit')::int;

-- name: GetSyncState :one
select * from user_sync_state
where user_id = $1 and project_id = $2 and kind = $3;

-- name: UpsertSyncState :exec
insert into user_sync_state (
  company_id, user_id, project_id, kind, high_water, last_pull_at,
  last_sweep_at, last_requested_at, last_error
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
on conflict (user_id, project_id, kind) do update set
  company_id = excluded.company_id,
  high_water = excluded.high_water,
  last_pull_at = excluded.last_pull_at,
  last_sweep_at = excluded.last_sweep_at,
  last_requested_at = excluded.last_requested_at,
  last_error = excluded.last_error;

-- name: ListWarmTargets :many
select * from user_sync_state
where last_requested_at > @requested_after
order by last_requested_at desc;

-- name: DeleteUserRecords :exec
with gone as (
  delete from user_sync_state
  where user_sync_state.user_id = @user_id and user_sync_state.project_id = @project_id and user_sync_state.kind = @kind
)
delete from user_records
where user_records.user_id = @user_id and user_records.project_id = @project_id and user_records.kind = @kind;
