-- name: InsertDraft :one
insert into drafts (
  id, company_id, project_id, user_id, feature, status, transcript, captured_at,
  llm_output, unresolved, confidence, model_version, prompt_version, extract_ms
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
returning *;

-- name: GetDraft :one
select * from drafts where id = $1;

-- name: GetDraftForUser :one
select * from drafts where id = $1 and user_id = $2;

-- name: ListDraftsForUserProject :many
select * from drafts
where user_id = $1 and project_id = $2
order by created_at desc
limit $3;

-- name: SetDraftStatus :exec
update drafts set status = $2, updated_at = now() where id = $1;

-- name: SetDraftJob :exec
update drafts set job_id = $2, updated_at = now() where id = $1;

-- name: ReplaceDraftJob :execrows
-- Points a submitting draft at a new job, but only while it still holds the
-- job the caller found finished ($2, null for none). Zero rows means
-- another submit replaced it first or the draft left submitting.
update drafts
set job_id = sqlc.arg(new_job_id), updated_at = now()
where id = sqlc.arg(id)
  and status = 'submitting'
  and job_id is not distinct from sqlc.narg(old_job_id);

-- name: OtherDraftByProcoreRecord :one
-- A draft other than $4 that already stores Procore record $3 in the project.
select id from drafts
where company_id = $1 and project_id = $2 and procore_record_id = $3 and id <> $4
limit 1;

-- name: SetDraftSubmitted :exec
update drafts
set status = 'submitted',
    procore_record_id = $2,
    procore_record_number = $3,
    procore_url = $4,
    error_code = null,
    error_message = null,
    updated_at = now()
where id = $1;

-- name: SetDraftFailed :exec
update drafts
set status = 'failed',
    error_code = $2,
    error_message = $3,
    unresolved = $4,
    updated_at = now()
where id = $1;

-- name: CountSubmittedDrafts :one
select count(*) from drafts where user_id = $1 and status = 'submitted';

-- name: SaveDraftEdits :one
-- Stores the reviewer's payload and its revalidated unresolved list unless
-- the draft is being or has been submitted; no row means it is locked. A
-- failed draft's error is cleared, since the edit supersedes it. When
-- if_updated_at is set, the draft must also be unchanged since then (no row
-- otherwise).
update drafts
set user_payload = sqlc.arg(user_payload),
    unresolved = sqlc.arg(unresolved),
    status = 'reviewed',
    error_code = null,
    error_message = null,
    updated_at = now()
where id = sqlc.arg(id)
  and status not in ('submitting', 'submitted')
  and (sqlc.narg(if_updated_at)::timestamptz is null or updated_at = sqlc.narg(if_updated_at))
returning *;

-- name: MarkDraftSubmitting :one
-- Moves a draft to submitting and clears any earlier job, but only if it is
-- not already submitting or submitted and is unchanged since the caller
-- validated it (updated_at = $2). No row means another submit or an edit won.
update drafts
set status = 'submitting',
    job_id = null,
    updated_at = now()
where id = $1
  and status not in ('submitting', 'submitted')
  and updated_at = $2
returning *;
