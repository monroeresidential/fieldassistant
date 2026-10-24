-- name: InsertAuditEvent :one
insert into audit_events (company_id, draft_id, kind, payload, procore_request, procore_response, duration_ms)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: ListAuditEvents :many
select * from audit_events where draft_id = $1 order by created_at, id;
