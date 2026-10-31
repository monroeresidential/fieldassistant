-- Read-only queries for cmd/pilotreport (internal/report).

-- name: ListDraftsInWindow :many
-- Drafts created in [from, to), oldest first.
select * from drafts
where created_at >= sqlc.arg(from_at) and created_at < sqlc.arg(to_at)
order by created_at, id;

-- name: ListAuditEventsForDrafts :many
-- The drafts' audit events of the given kinds written before before_at,
-- in trail order per draft. Payloads of other kinds (extracted turns,
-- edited before/after) are not needed and not fetched.
select id, draft_id, kind, payload, procore_response, created_at
from audit_events
where draft_id = any(sqlc.arg(draft_ids)::uuid[])
  and kind = any(sqlc.arg(kinds)::text[])
  and created_at < sqlc.arg(before_at)
order by draft_id, created_at, id;

-- name: ListAttachmentsForDrafts :many
select draft_id, state, error_code
from attachments
where draft_id = any(sqlc.arg(draft_ids)::uuid[])
order by draft_id, created_at, id;

-- name: ListUsersByIDs :many
-- Names only; the report never prints emails.
select id, procore_user_id, name
from users
where id = any(sqlc.arg(ids)::uuid[]);

-- name: ListMirrorRFIsForProjectSubjects :many
-- Live RFIs in any user's mirror of the given projects whose subject is one
-- of subjects, once per RFI, leaving out RFIs that a draft already stores
-- as its Procore record (those are that draft's own RFI).
select distinct on (r.company_id, r.project_id, r.procore_id)
  r.company_id,
  r.project_id,
  r.procore_id,
  coalesce(r.body->>'subject', '')::text as subject,
  coalesce((r.body->'created_by'->>'id')::bigint, 0)::bigint as created_by_id
from user_records r
where r.kind = 'rfi'
  and r.deleted_at is null
  and r.project_id = any(sqlc.arg(project_ids)::bigint[])
  and r.body->>'subject' = any(sqlc.arg(subjects)::text[])
  and not exists (
    select 1 from drafts d
    where d.company_id = r.company_id and d.procore_record_id = r.procore_id
  )
order by r.company_id, r.project_id, r.procore_id;
