package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// Attachment states and limits.
const (
	// AttachmentPending is a manifest entry whose bytes have not arrived.
	AttachmentPending = "pending"
	// AttachmentStored means the bytes are in the object store.
	AttachmentStored = "stored"
	// AttachmentUploaded means the bytes were pushed to Procore.
	AttachmentUploaded = "uploaded"
	// AttachmentFailed means a push failed definitively; see Error.
	AttachmentFailed = "failed"

	MaxAttachments     = 10
	MaxAttachmentBytes = 20 << 20
	// AttachmentsField is the unresolved field name for photo failures.
	AttachmentsField = "attachments"
)

// attachmentContentType is the only content type an attachment may have.
const attachmentContentType = "image/jpeg"

// jpegMagic is the start of every JPEG file.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

var (
	// ErrAttachmentInvalid means a body is not a JPEG or is over
	// MaxAttachmentBytes; nothing was stored.
	ErrAttachmentInvalid = errors.New("pipeline: attachment is not a jpeg within the size limit")
	// ErrTooManyAttachments means a manifest lists more than MaxAttachments.
	ErrTooManyAttachments = errors.New("pipeline: more than 10 attachments")
	// ErrAttachmentsPending means a draft has photos that are not stored
	// yet or failed; see PendingError.
	ErrAttachmentsPending = errors.New("pipeline: attachments not uploaded")
	// ErrAttachmentNotFound means the user's draft has no attachment with
	// that ID. It wraps ErrNotFound, so a caller can tell it from a missing
	// draft (which is ErrNotFound itself).
	ErrAttachmentNotFound = fmt.Errorf("%w: attachment is not on this draft", ErrNotFound)
)

// PendingError is Submit's refusal of a draft with N photos that are pending
// or failed. It wraps ErrAttachmentsPending.
type PendingError struct{ N int }

func (e *PendingError) Error() string {
	if e.N == 1 {
		return "1 photo has not finished uploading"
	}
	return fmt.Sprintf("%d photos have not finished uploading", e.N)
}

func (e *PendingError) Unwrap() error { return ErrAttachmentsPending }

// AttachmentInput is one manifest entry of a new draft. ID is chosen by the
// client.
type AttachmentInput struct {
	ID                    uuid.UUID
	Filename, ContentType string
	Bytes                 int64
}

// Attachment is one photo on a draft.
type Attachment struct {
	ID, DraftID           uuid.UUID
	Filename, ContentType string
	// Bytes is the manifest's size until the body is stored, then the
	// counted size.
	Bytes int64
	// ObjectKey is set once stored; ProcoreUploadID and UploadedAt once
	// uploaded.
	ObjectKey, State, ProcoreUploadID string
	UploadedAt                        time.Time
	// Error is why a failed attachment could not be pushed.
	Error     *DraftError
	CreatedAt time.Time
}

// insertManifest stores a new draft's manifest as pending attachments in
// q's transaction. Each entry is a microsecond after the one before, so
// manifest order survives the shared transaction time.
func (d *Drafts) insertManifest(ctx context.Context, q *db.Queries, in CreateInput) error {
	base := d.now().Truncate(time.Microsecond)
	for i, a := range in.Attachments {
		_, err := q.InsertAttachment(ctx, db.InsertAttachmentParams{
			ID:          pgtype.UUID{Bytes: a.ID, Valid: true},
			CompanyID:   in.CompanyID,
			DraftID:     pgtype.UUID{Bytes: in.ID, Valid: true},
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Bytes:       a.Bytes,
			CreatedAt:   pgtype.Timestamptz{Time: base.Add(time.Duration(i) * time.Microsecond), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("pipeline: store attachment %s: %w", a.ID, err)
		}
	}
	return nil
}

// StoreAttachment writes the body of one of the user's draft's attachments
// to the object store and marks it stored. The body must be a JPEG
// (contentType image/jpeg, first bytes FF D8 FF) of at most
// MaxAttachmentBytes, or the call is ErrAttachmentInvalid and nothing is
// stored. The body is read in full before anything is written, so the store
// never holds a partial object. An attachment already stored or uploaded is
// returned unchanged without reading the body. A submitting or submitted
// draft is ErrDraftLocked; an attachment not on the draft is
// ErrAttachmentNotFound.
func (d *Drafts) StoreAttachment(ctx context.Context, userID, draftID, attachmentID uuid.UUID, contentType string, body io.Reader) (Attachment, error) {
	cur, row, err := d.lockedAttachment(ctx, userID, draftID, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	if row.State == AttachmentStored || row.State == AttachmentUploaded {
		return toAttachment(row), nil
	}
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || mt != attachmentContentType {
		return Attachment{}, ErrAttachmentInvalid
	}
	buf, err := io.ReadAll(io.LimitReader(body, MaxAttachmentBytes+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("pipeline: read attachment: %w", err)
	}
	if len(buf) > MaxAttachmentBytes || !bytes.HasPrefix(buf, jpegMagic) {
		return Attachment{}, ErrAttachmentInvalid
	}
	key := blob.Key(cur.CompanyID, draftID, attachmentID)
	// A seekable reader with the exact size: the S3 client needs both over
	// plain HTTP.
	if err := d.blobs.Put(ctx, key, attachmentContentType, int64(len(buf)), bytes.NewReader(buf)); err != nil {
		return Attachment{}, fmt.Errorf("pipeline: store attachment object: %w", err)
	}
	if err := d.st.Q.SetAttachmentStored(ctx, db.SetAttachmentStoredParams{
		ID:        row.ID,
		ObjectKey: pgtype.Text{String: key, Valid: true},
		Bytes:     int64(len(buf)),
	}); err != nil {
		return Attachment{}, fmt.Errorf("pipeline: mark attachment stored: %w", err)
	}
	row, err = d.st.Q.GetAttachmentForDraft(ctx, db.GetAttachmentForDraftParams{ID: row.ID, DraftID: row.DraftID})
	if err != nil {
		return Attachment{}, fmt.Errorf("pipeline: load attachment: %w", err)
	}
	return toAttachment(row), nil
}

// DeleteAttachment removes one of the user's draft's attachments and its
// object. A photo failure flagged on the draft (an unresolved entry on
// AttachmentsField) is cleared first, so a later submit does not report a
// photo that is gone (see clearAttachmentFlags). The object is deleted best
// effort: a failure is logged and the row is still gone. A submitting or
// submitted draft is ErrDraftLocked; an attachment not on the draft is
// ErrAttachmentNotFound.
func (d *Drafts) DeleteAttachment(ctx context.Context, userID, draftID, attachmentID uuid.UUID) error {
	cur, row, err := d.lockedAttachment(ctx, userID, draftID, attachmentID)
	if err != nil {
		return err
	}
	if err := d.clearAttachmentFlags(ctx, cur); err != nil {
		return err
	}
	if err := d.st.Q.DeleteAttachment(ctx, db.DeleteAttachmentParams{ID: row.ID, DraftID: row.DraftID}); err != nil {
		return fmt.Errorf("pipeline: delete attachment: %w", err)
	}
	key := blob.Key(row.CompanyID, draftID, attachmentID)
	if err := d.blobs.Delete(ctx, key); err != nil {
		d.log.Warn("delete attachment object", "draft_id", draftID, "attachment_id", attachmentID, "key", key, "error", err)
	}
	return nil
}

// clearAttachmentFlags drops cur's unresolved entries on AttachmentsField
// and stores the rest as SaveEdits stores an edit of an unchanged payload:
// status reviewed, the failure error cleared, an edited audit event. It
// writes nothing when there is no such entry. A draft that became submitting
// or submitted since cur was read is ErrDraftLocked.
func (d *Drafts) clearAttachmentFlags(ctx context.Context, cur Draft) error {
	kept := make([]Unresolved, 0, len(cur.Unresolved))
	for _, u := range cur.Unresolved {
		if u.Field != AttachmentsField {
			kept = append(kept, u)
		}
	}
	if len(kept) == len(cur.Unresolved) {
		return nil
	}
	payload := map[string]any{}
	if raw := cur.Payload(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("pipeline: decode payload: %w", err)
		}
	}
	_, err := d.storeEdits(ctx, cur, payload, kept, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDraftLocked
	}
	return err
}

// lockedAttachment loads the user's draft and one of its attachments for a
// change: ErrNotFound for another user's or a missing draft, ErrDraftLocked
// while the draft is submitting or submitted, ErrAttachmentNotFound when the
// attachment is not on it.
func (d *Drafts) lockedAttachment(ctx context.Context, userID, draftID, attachmentID uuid.UUID) (Draft, db.Attachment, error) {
	cur, err := d.Get(ctx, userID, draftID)
	if err != nil {
		return Draft{}, db.Attachment{}, err
	}
	if cur.Status == StatusSubmitting || cur.Status == StatusSubmitted {
		return Draft{}, db.Attachment{}, ErrDraftLocked
	}
	row, err := d.st.Q.GetAttachmentForDraft(ctx, db.GetAttachmentForDraftParams{
		ID: pgtype.UUID{Bytes: attachmentID, Valid: true}, DraftID: pgtype.UUID{Bytes: draftID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, db.Attachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return Draft{}, db.Attachment{}, fmt.Errorf("pipeline: load attachment: %w", err)
	}
	return cur, row, nil
}

// checkAttachments refuses a submit of a draft while any of its attachments
// is pending or failed, with *PendingError. It writes nothing. Submit calls
// it only for a draft that is not submitting or submitted.
func (d *Drafts) checkAttachments(ctx context.Context, draftID uuid.UUID) error {
	n, err := d.st.Q.CountAttachmentsNotUploadable(ctx, pgtype.UUID{Bytes: draftID, Valid: true})
	if err != nil {
		return fmt.Errorf("pipeline: count pending attachments: %w", err)
	}
	if n > 0 {
		return &PendingError{N: int(n)}
	}
	return nil
}

// attachments loads a draft's attachments in manifest order.
func (d *Drafts) attachments(ctx context.Context, draftID uuid.UUID) ([]Attachment, error) {
	rows, err := d.st.Q.ListAttachmentsForDraft(ctx, pgtype.UUID{Bytes: draftID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("pipeline: list attachments: %w", err)
	}
	out := make([]Attachment, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAttachment(r))
	}
	return out, nil
}

func toAttachment(r db.Attachment) Attachment {
	a := Attachment{
		ID:              uuid.UUID(r.ID.Bytes),
		DraftID:         uuid.UUID(r.DraftID.Bytes),
		Filename:        r.Filename,
		ContentType:     r.ContentType,
		Bytes:           r.Bytes,
		ObjectKey:       r.ObjectKey.String,
		State:           r.State,
		ProcoreUploadID: r.ProcoreUploadID.String,
		UploadedAt:      r.UploadedAt.Time,
		CreatedAt:       r.CreatedAt.Time,
	}
	if r.ErrorCode.Valid {
		a.Error = &DraftError{Code: r.ErrorCode.String, Message: r.ErrorMessage.String}
	}
	return a
}
