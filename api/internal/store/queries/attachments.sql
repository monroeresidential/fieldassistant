-- name: InsertAttachment :one
-- created_at is set by the caller so a manifest inserted in one
-- transaction keeps its order.
insert into attachments (id, company_id, draft_id, filename, content_type, bytes, created_at)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: ListAttachmentsForDraft :many
-- A draft's attachments in manifest order.
select * from attachments
where draft_id = $1
order by created_at, id;

-- name: GetAttachmentForDraft :one
select * from attachments where id = $1 and draft_id = $2;

-- name: SetAttachmentStored :exec
-- Records that the bytes are in the object store under object_key.
update attachments
set object_key = $2,
    bytes = $3,
    state = 'stored',
    error_code = null,
    error_message = null,
    updated_at = now()
where id = $1;

-- name: SetAttachmentUploaded :exec
-- Records the Procore upload id the photo was pushed under.
update attachments
set procore_upload_id = $2,
    uploaded_at = $3,
    state = 'uploaded',
    error_code = null,
    error_message = null,
    updated_at = now()
where id = $1;

-- name: SetAttachmentFailed :exec
update attachments
set state = 'failed',
    error_code = $2,
    error_message = $3,
    updated_at = now()
where id = $1;

-- name: DeleteAttachment :exec
delete from attachments where id = $1 and draft_id = $2;

-- name: CountAttachmentsNotUploadable :one
-- Attachments that block a submit: bytes not yet stored, or a push that
-- failed definitively.
select count(*) from attachments
where draft_id = $1 and state in ('pending', 'failed');
