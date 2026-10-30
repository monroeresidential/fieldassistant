package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// Error codes stored on an attachment whose push failed definitively.
const (
	// AttachmentErrProcoreRefused means Procore or its storage service
	// refused the photo.
	AttachmentErrProcoreRefused = "procore_refused"
	// AttachmentErrExpired means the photo's bytes are gone from the object
	// store.
	AttachmentErrExpired = "expired"
	// AttachmentErrSizeMismatch means the object in the store does not have
	// the size the attachment's record holds.
	AttachmentErrSizeMismatch = "size_mismatch"
)

// errSizeMismatch means a stored object's size differs from its record.
var errSizeMismatch = errors.New("pipeline: stored object size differs from the attachment record")

// UploadReuseWindow is how long a photo's Procore upload id is reused by a
// later submit attempt; an older one is pushed again.
const UploadReuseWindow = 5 * 24 * time.Hour

// Messages of a definitive photo failure; %s is the photo's filename.
const (
	attachmentRefusedMessage      = "Photo %s could not be sent to Procore. Remove it and submit again."
	attachmentExpiredMessage      = "Photo %s expired on the server. Remove it and submit again."
	attachmentSizeMismatchMessage = "Photo %s on the server does not match its record. Remove it and submit again."
	// attachmentUncoveredMessage takes the earlier RFI's number (or id),
	// then the filename.
	attachmentUncoveredMessage = "An earlier attempt created RFI %s without photo %s. Remove the photo and submit again, or add it in Procore."
)

// transientStorageCodes are S3 error codes the storage service answers with
// a 4xx that a later attempt may get past: a slow or clock-skewed request,
// expired credentials, or throttling.
var transientStorageCodes = []string{"RequestTimeout", "RequestTimeTooSkewed", "ExpiredToken", "SlowDown"}

// transientStorageError reports whether an error body from the storage
// service carries one of transientStorageCodes.
func transientStorageError(api *procore.APIError) bool {
	for _, code := range transientStorageCodes {
		if strings.Contains(api.Body, "<Code>"+code+"</Code>") {
			return true
		}
	}
	return false
}

// uploadedAudit is the payload of an uploaded audit event.
type uploadedAudit struct {
	AttachmentID    uuid.UUID `json:"attachment_id"`
	ProcoreUploadID string    `json:"procore_upload_id"`
	Bytes           int64     `json:"bytes"`
	DurationMS      int       `json:"duration_ms"`
}

// pushAttachments pushes every stored attachment of cur to Procore in manifest order and
// returns the upload ids. A *submitFailure means a definitive photo failure already recorded
// on the attachment (state failed) and to be applied to the draft by the caller; an error is
// transient and retried by River.
//
// An attachment uploaded within UploadReuseWindow of now keeps its upload
// id. Any other is read from the object store, given a new Direct File
// Upload with the submitting user's access token, fetched afresh for each
// photo so a long push never outlives it, and posted to Procore's storage,
// then marked uploaded with an uploaded audit event. The push stops at the
// first failure; attachments pushed before it keep their ids for the next
// attempt. A lost token or a 401 is a reauth_required failure and a 403 from
// Procore a procore_permission_denied one, neither recorded on the
// attachment.
func (d *Drafts) pushAttachments(ctx context.Context, cur Draft) (ids []string, fail *submitFailure, err error) {
	for _, att := range cur.Attachments {
		if att.State == AttachmentUploaded && att.ProcoreUploadID != "" && d.now().Sub(att.UploadedAt) < UploadReuseWindow {
			ids = append(ids, att.ProcoreUploadID)
			continue
		}
		id, err := d.pushAttachment(ctx, cur, att)
		if err != nil {
			fail, err := d.attachmentFailure(ctx, cur, att, err)
			return nil, fail, err
		}
		ids = append(ids, id)
	}
	return ids, nil, nil
}

// pushAttachment sends one attachment's object to Procore and stores the
// upload id it went up under, and returns that id. An object whose size is
// not the record's is errSizeMismatch, before anything is sent.
func (d *Drafts) pushAttachment(ctx context.Context, cur Draft, att Attachment) (string, error) {
	start := time.Now()
	rc, size, err := d.blobs.Get(ctx, att.ObjectKey)
	if err != nil {
		return "", fmt.Errorf("pipeline: read attachment %s: %w", att.ID, err)
	}
	defer rc.Close()
	if size != att.Bytes {
		return "", fmt.Errorf("pipeline: attachment %s object has %d bytes, record %d: %w", att.ID, size, att.Bytes, errSizeMismatch)
	}
	token, err := d.tokens.AccessToken(ctx, cur.UserID)
	if err != nil {
		return "", fmt.Errorf("pipeline: access token for attachment %s: %w", att.ID, err)
	}
	up, err := d.procore.CreateUpload(ctx, token, cur.CompanyID, cur.ProjectID, procore.UploadCreate{
		ResponseFilename: att.Filename, ResponseContentType: attachmentContentType,
	})
	if err != nil {
		return "", fmt.Errorf("pipeline: create upload for attachment %s: %w", att.ID, err)
	}
	if err := d.procore.PostUpload(ctx, up, att.Filename, size, rc); err != nil {
		return "", fmt.Errorf("pipeline: post attachment %s: %w", att.ID, err)
	}
	ms := int(time.Since(start).Milliseconds())
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.SetAttachmentUploaded(ctx, db.SetAttachmentUploadedParams{
			ID:              pgtype.UUID{Bytes: att.ID, Valid: true},
			ProcoreUploadID: pgtype.Text{String: up.UUID, Valid: true},
			UploadedAt:      pgtype.Timestamptz{Time: d.now(), Valid: true},
		}); err != nil {
			return fmt.Errorf("pipeline: mark attachment %s uploaded: %w", att.ID, err)
		}
		return insertAudit(ctx, q, cur.CompanyID, cur.ID, AuditUploaded, uploadedAudit{
			AttachmentID: att.ID, ProcoreUploadID: up.UUID, Bytes: size, DurationMS: ms,
		}, ms)
	})
	if err != nil {
		return "", err
	}
	return up.UUID, nil
}

// attachmentFailure classifies a failed push of att. A refusal (a 4xx other
// than 408 and 429 from creating the upload or from storage, unless storage
// names one of transientStorageCodes), an object gone from the store or an
// object whose size is not the record's marks att failed and returns the draft's
// procore_validation failure on AttachmentsField. A lost token, a 401 or a
// 403 from Procore returns the draft failure RunSubmit uses for them. Any
// other error is returned as is, to be retried.
func (d *Drafts) attachmentFailure(ctx context.Context, cur Draft, att Attachment, err error) (*submitFailure, error) {
	var code, format string
	var ve *procore.ValidationError
	var api *procore.APIError
	switch {
	case errors.Is(err, procore.ErrUnauthorized), errors.Is(err, auth.ErrReauthRequired):
		return &submitFailure{code: CodeReauthRequired, message: "Sign in to Procore again, then submit.", cause: err}, nil
	case errors.Is(err, procore.ErrPermissionDenied):
		return &submitFailure{code: CodePermissionDenied, message: "Your Procore account cannot create RFIs in this project.", cause: err}, nil
	case errors.Is(err, blob.ErrNotFound):
		code, format = AttachmentErrExpired, attachmentExpiredMessage
	case errors.Is(err, errSizeMismatch):
		code, format = AttachmentErrSizeMismatch, attachmentSizeMismatchMessage
	case errors.As(err, &ve), errors.As(err, &api) && definitiveStatus(api.Status) && !transientStorageError(api):
		code, format = AttachmentErrProcoreRefused, attachmentRefusedMessage
	default:
		return nil, err
	}
	msg := fmt.Sprintf(format, att.Filename)
	if err := d.st.Q.SetAttachmentFailed(ctx, db.SetAttachmentFailedParams{
		ID:           pgtype.UUID{Bytes: att.ID, Valid: true},
		ErrorCode:    pgtype.Text{String: code, Valid: true},
		ErrorMessage: pgtype.Text{String: msg, Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("pipeline: mark attachment %s failed: %w", att.ID, err)
	}
	return &submitFailure{
		code:    CodeProcoreValidation,
		message: msg,
		cause:   err,
		invalid: []Unresolved{{Field: AttachmentsField, Reason: ReasonProcoreValidation, Message: msg, Candidates: []LookupRow{}}},
	}, nil
}

// uncoveredPhotoFailure checks, before adopting an earlier attempt's RFI
// (rfi), that the attempts of that run posted every photo cur holds: each
// stored or uploaded attachment needs an upload id among uploadIDs. For the
// first one that has none, it returns the draft's failure, naming the RFI by
// its number (its id when it has none); the failure keeps the objects and
// does not close the unknown run, so a submit after the photo is removed
// adopts the RFI and none ever posts a second one. It returns nil when every
// photo is covered.
func uncoveredPhotoFailure(cur Draft, rfi procore.RFI, uploadIDs []string) *submitFailure {
	for _, a := range cur.Attachments {
		if a.State != AttachmentStored && a.State != AttachmentUploaded {
			continue
		}
		if a.ProcoreUploadID != "" && slices.Contains(uploadIDs, a.ProcoreUploadID) {
			continue
		}
		label := rfi.Number
		if label == "" {
			label = strconv.FormatInt(rfi.ID, 10)
		}
		msg := fmt.Sprintf(attachmentUncoveredMessage, label, a.Filename)
		return &submitFailure{
			code:    CodeProcoreValidation,
			message: msg,
			cause:   fmt.Errorf("pipeline: attachment %s is not on the earlier attempt's rfi", a.ID),
			invalid: []Unresolved{{Field: AttachmentsField, Reason: ReasonProcoreValidation, Message: msg, Candidates: []LookupRow{}}},
		}
	}
	return nil
}

// deleteAttachmentObjects deletes the objects of cur's attachments once the
// draft is submitted, best effort: a failure is logged and the object left.
// The rows stay uploaded with their upload ids.
func (d *Drafts) deleteAttachmentObjects(ctx context.Context, cur Draft) {
	for _, a := range cur.Attachments {
		if a.ObjectKey == "" {
			continue
		}
		if err := d.blobs.Delete(ctx, a.ObjectKey); err != nil {
			d.log.Warn("delete submitted attachment object", "draft_id", cur.ID, "attachment_id", a.ID, "key", a.ObjectKey, "error", err)
		}
	}
}
