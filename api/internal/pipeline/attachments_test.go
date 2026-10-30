package pipeline

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// jpegBody is a body that starts with the JPEG marker bytes.
func jpegBody(n int) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, n)...)
}

func photoInputs(n int) []AttachmentInput {
	out := make([]AttachmentInput, n)
	for i := range out {
		out[i] = AttachmentInput{ID: uuid.New(), Filename: "IMG_" + string(rune('A'+i)) + ".jpg", ContentType: "image/jpeg", Bytes: 1000}
	}
	return out
}

// withPhotos creates a submittable draft (nothing unresolved) whose manifest
// lists n photos.
func (e *draftsEnv) withPhotos(t *testing.T, n int) Draft {
	t.Helper()
	e.fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	in := e.input(uuid.New())
	in.Attachments = photoInputs(n)
	d, err := e.drafts.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(d.Unresolved) != 0 {
		t.Fatalf("draft has unresolved fields: %+v", d.Unresolved)
	}
	return d
}

func (e *draftsEnv) setStatus(t *testing.T, id uuid.UUID, status string) {
	t.Helper()
	if err := e.st.Q.SetDraftStatus(context.Background(), db.SetDraftStatusParams{ID: pgtype.UUID{Bytes: id, Valid: true}, Status: status}); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func TestCreate_StoresManifestPending(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	in := e.input(uuid.New())
	in.Attachments = []AttachmentInput{
		{ID: uuid.New(), Filename: "first.jpg", ContentType: "image/jpeg", Bytes: 1200},
		{ID: uuid.New(), Filename: "second.jpg", ContentType: "image/jpeg", Bytes: 3400},
	}
	d, err := e.drafts.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, got := range []Draft{d, e.mustGet(t, d.ID)} {
		if len(got.Attachments) != 2 {
			t.Fatalf("attachments = %+v, want 2", got.Attachments)
		}
		for i, a := range got.Attachments {
			want := in.Attachments[i]
			if a.ID != want.ID || a.DraftID != d.ID || a.Filename != want.Filename || a.ContentType != want.ContentType ||
				a.Bytes != want.Bytes || a.State != AttachmentPending || a.ObjectKey != "" || a.Error != nil {
				t.Errorf("attachment %d = %+v, want pending %+v", i, a, want)
			}
		}
	}
	rows, err := e.st.Q.ListAttachmentsForDraft(context.Background(), pgtype.UUID{Bytes: d.ID, Valid: true})
	if err != nil || len(rows) != 2 || rows[0].CompanyID != testCompany {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestCreate_ElevenAttachments_Rejected(t *testing.T) {
	e := newDraftsEnv(t)
	in := e.input(uuid.New())
	in.Attachments = photoInputs(MaxAttachments + 1)
	if _, err := e.drafts.Create(context.Background(), in); !errors.Is(err, ErrTooManyAttachments) {
		t.Fatalf("Create = %v, want ErrTooManyAttachments", err)
	}
	if _, err := e.drafts.Get(context.Background(), e.user, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

func TestStoreAttachment_WritesObjectAndMarksStored(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	att := d.Attachments[0]
	body := jpegBody(5000)

	got, err := e.drafts.StoreAttachment(context.Background(), e.user, d.ID, att.ID, "image/jpeg", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("StoreAttachment: %v", err)
	}
	key := blob.Key(testCompany, d.ID, att.ID)
	if got.State != AttachmentStored || got.ObjectKey != key || got.Bytes != int64(len(body)) || got.Error != nil {
		t.Fatalf("attachment = %+v", got)
	}
	if keys := e.blobs.Keys(); !reflect.DeepEqual(keys, []string{key}) {
		t.Fatalf("keys = %v, want [%s]", keys, key)
	}
	rc, size, err := e.blobs.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get object: %v", err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil || size != int64(len(body)) || !bytes.Equal(buf.Bytes(), body) {
		t.Fatalf("object = %d bytes (size %d), %v", buf.Len(), size, err)
	}
	if a := e.mustGet(t, d.ID).Attachments[0]; a.State != AttachmentStored || a.ObjectKey != key {
		t.Errorf("draft attachment = %+v", a)
	}
}

func TestStoreAttachment_Idempotent(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	att := d.Attachments[0]
	ctx := context.Background()
	first, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, att.ID, "image/jpeg", bytes.NewReader(jpegBody(100)))
	if err != nil {
		t.Fatalf("first StoreAttachment: %v", err)
	}
	second, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, att.ID, "image/jpeg", bytes.NewReader(jpegBody(999)))
	if err != nil {
		t.Fatalf("second StoreAttachment: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second = %+v, want unchanged %+v", second, first)
	}
	if keys := e.blobs.Keys(); len(keys) != 1 {
		t.Fatalf("keys = %v, want one", keys)
	}
	_, size, err := e.blobs.Get(ctx, first.ObjectKey)
	if err != nil || size != first.Bytes {
		t.Fatalf("object size = %d, %v; want %d", size, err, first.Bytes)
	}
}

func TestStoreAttachment_NotJPEG_Invalid(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	att := d.Attachments[0]
	ctx := context.Background()
	if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, att.ID, "image/jpeg", strings.NewReader("\x89PNG\r\n\x1a\nrest")); !errors.Is(err, ErrAttachmentInvalid) {
		t.Fatalf("PNG body = %v, want ErrAttachmentInvalid", err)
	}
	if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, att.ID, "image/png", bytes.NewReader(jpegBody(10))); !errors.Is(err, ErrAttachmentInvalid) {
		t.Fatalf("image/png content type = %v, want ErrAttachmentInvalid", err)
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
	if a := e.mustGet(t, d.ID).Attachments[0]; a.State != AttachmentPending {
		t.Errorf("state = %q, want pending", a.State)
	}
}

func TestStoreAttachment_UnknownID_NotFound(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	ctx := context.Background()
	if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, uuid.New(), "image/jpeg", bytes.NewReader(jpegBody(10))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown attachment = %v, want ErrNotFound", err)
	}
	other := newDBUser(t, e.st, 99, "Other Person")
	if _, err := e.drafts.StoreAttachment(ctx, other, d.ID, d.Attachments[0].ID, "image/jpeg", bytes.NewReader(jpegBody(10))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's draft = %v, want ErrNotFound", err)
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
}

func TestStoreAttachment_Locked(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	e.setStatus(t, d.ID, StatusSubmitting)
	if _, err := e.drafts.StoreAttachment(context.Background(), e.user, d.ID, d.Attachments[0].ID, "image/jpeg", bytes.NewReader(jpegBody(10))); !errors.Is(err, ErrDraftLocked) {
		t.Fatalf("StoreAttachment = %v, want ErrDraftLocked", err)
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
}

func TestDeleteAttachment_RemovesRowAndObject(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 2)
	ctx := context.Background()
	gone, kept := d.Attachments[0], d.Attachments[1]
	for _, a := range d.Attachments {
		if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, a.ID, "image/jpeg", bytes.NewReader(jpegBody(10))); err != nil {
			t.Fatalf("StoreAttachment: %v", err)
		}
	}
	if err := e.drafts.DeleteAttachment(ctx, e.user, d.ID, gone.ID); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	got := e.mustGet(t, d.ID)
	if len(got.Attachments) != 1 || got.Attachments[0].ID != kept.ID {
		t.Fatalf("attachments = %+v, want only %s", got.Attachments, kept.ID)
	}
	if keys := e.blobs.Keys(); !reflect.DeepEqual(keys, []string{blob.Key(testCompany, d.ID, kept.ID)}) {
		t.Fatalf("keys = %v", keys)
	}
	if err := e.drafts.DeleteAttachment(ctx, e.user, d.ID, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	other := newDBUser(t, e.st, 99, "Other Person")
	if err := e.drafts.DeleteAttachment(ctx, other, d.ID, kept.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's delete = %v, want ErrNotFound", err)
	}
	e.setStatus(t, d.ID, StatusSubmitted)
	if err := e.drafts.DeleteAttachment(ctx, e.user, d.ID, kept.ID); !errors.Is(err, ErrDraftLocked) {
		t.Fatalf("delete on submitted draft = %v, want ErrDraftLocked", err)
	}
}

func TestDeleteAttachment_AllowedWhenDraftFailed(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	e.setStatus(t, d.ID, StatusFailed)
	if err := e.drafts.DeleteAttachment(context.Background(), e.user, d.ID, d.Attachments[0].ID); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	if got := e.mustGet(t, d.ID); len(got.Attachments) != 0 {
		t.Fatalf("attachments = %+v, want none", got.Attachments)
	}
}

func TestSubmit_PendingAttachment_Refused(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	ctx := context.Background()
	if _, err := e.drafts.SaveEdits(ctx, e.user, d.ID, d.Payload()); err != nil {
		t.Fatalf("SaveEdits: %v", err)
	}
	before := len(auditEvents(t, e.st, d.ID))
	q := &enqueuer{}

	_, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if !errors.Is(err, ErrAttachmentsPending) {
		t.Fatalf("Submit = %v, want ErrAttachmentsPending", err)
	}
	var pe *PendingError
	if !errors.As(err, &pe) || pe.N != 1 || err.Error() != "1 photo has not finished uploading" {
		t.Fatalf("Submit = %#v (%q), want PendingError{N: 1}", err, err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusReviewed || got.JobID != 0 {
		t.Fatalf("draft = status %q job %d, want reviewed with no job", got.Status, got.JobID)
	}
	if len(q.drafts) != 0 {
		t.Errorf("enqueued %v", q.drafts)
	}
	if after := len(auditEvents(t, e.st, d.ID)); after != before {
		t.Errorf("audit events %d → %d, want none added", before, after)
	}
	if got := (&PendingError{N: 2}).Error(); got != "2 photos have not finished uploading" {
		t.Errorf("PendingError{2} = %q", got)
	}
}

func TestSubmit_FailedAttachment_Refused(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 2)
	ctx := context.Background()
	if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, d.Attachments[0].ID, "image/jpeg", bytes.NewReader(jpegBody(10))); err != nil {
		t.Fatalf("StoreAttachment: %v", err)
	}
	if err := e.st.Q.SetAttachmentFailed(ctx, db.SetAttachmentFailedParams{
		ID:           pgtype.UUID{Bytes: d.Attachments[1].ID, Valid: true},
		ErrorCode:    pgtype.Text{String: "procore_refused", Valid: true},
		ErrorMessage: pgtype.Text{String: "Photo IMG_B.jpg could not be sent to Procore.", Valid: true},
	}); err != nil {
		t.Fatalf("SetAttachmentFailed: %v", err)
	}
	got := e.mustGet(t, d.ID).Attachments[1]
	if got.State != AttachmentFailed || got.Error == nil || got.Error.Code != "procore_refused" {
		t.Fatalf("attachment = %+v", got)
	}
	q := &enqueuer{}
	_, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	var pe *PendingError
	if !errors.Is(err, ErrAttachmentsPending) || !errors.As(err, &pe) || pe.N != 1 {
		t.Fatalf("Submit = %v, want PendingError{N: 1}", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusExtracted || len(q.drafts) != 0 {
		t.Fatalf("draft status %q, enqueued %v", got.Status, q.drafts)
	}
}

func TestSubmit_AlreadySubmitted_IgnoresAttachmentGuard(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.withPhotos(t, 1)
	ctx := context.Background()
	if err := e.st.Q.SetAttachmentFailed(ctx, db.SetAttachmentFailedParams{
		ID:           pgtype.UUID{Bytes: d.Attachments[0].ID, Valid: true},
		ErrorCode:    pgtype.Text{String: "procore_refused", Valid: true},
		ErrorMessage: pgtype.Text{String: "Photo IMG_A.jpg could not be sent to Procore.", Valid: true},
	}); err != nil {
		t.Fatalf("SetAttachmentFailed: %v", err)
	}
	e.setStatus(t, d.ID, StatusSubmitted)
	q := &enqueuer{}

	out, err := e.drafts.Submit(ctx, e.user, d.ID, q)
	if err != nil {
		t.Fatalf("Submit = %v, want the already-submitted outcome", err)
	}
	if !out.AlreadySubmitted || out.Draft.ID != d.ID {
		t.Fatalf("Submit = %+v, want AlreadySubmitted for draft %s", out, d.ID)
	}
	if len(q.drafts) != 0 {
		t.Errorf("enqueued %v", q.drafts)
	}
}
