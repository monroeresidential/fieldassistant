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

-- name: SetDraftUserPayload :exec
update drafts set user_payload = $2, updated_at = now() where id = $1;

-- name: SetDraftStatus :exec
update drafts set status = $2, updated_at = now() where id = $1;

-- name: SetDraftJob :exec
update drafts set job_id = $2, updated_at = now() where id = $1;

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
