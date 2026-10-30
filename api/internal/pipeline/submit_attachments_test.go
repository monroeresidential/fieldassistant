package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// storedPhotos creates a submittable draft with n photos whose bytes are in
// the object store, and returns it with each photo's body in manifest order.
func (e *draftsEnv) storedPhotos(t *testing.T, n int) (Draft, [][]byte) {
	t.Helper()
	d := e.withPhotos(t, n)
	ctx := context.Background()
	bodies := make([][]byte, n)
	for i, a := range d.Attachments {
		bodies[i] = jpegBody(100 + 10*i)
		if _, err := e.drafts.StoreAttachment(ctx, e.user, d.ID, a.ID, "image/jpeg", bytes.NewReader(bodies[i])); err != nil {
			t.Fatalf("StoreAttachment %d: %v", i, err)
		}
	}
	return e.mustGet(t, d.ID), bodies
}

// draftAttachments loads the draft's attachments in manifest order.
func (e *draftsEnv) draftAttachments(t *testing.T, id uuid.UUID) []Attachment {
	t.Helper()
	atts, err := e.drafts.attachments(context.Background(), id)
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	return atts
}

// setUploaded marks an attachment uploaded under uploadID at at.
func (e *draftsEnv) setUploaded(t *testing.T, id uuid.UUID, uploadID string, at time.Time) {
	t.Helper()
	if err := e.st.Q.SetAttachmentUploaded(context.Background(), db.SetAttachmentUploadedParams{
		ID:              pgtype.UUID{Bytes: id, Valid: true},
		ProcoreUploadID: pgtype.Text{String: uploadID, Valid: true},
		UploadedAt:      pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		t.Fatalf("set uploaded: %v", err)
	}
}

// auditsOfKind returns the draft's audit events of kind, oldest first.
func auditsOfKind(t *testing.T, e *draftsEnv, id uuid.UUID, kind string) []db.AuditEvent {
	t.Helper()
	var out []db.AuditEvent
	for _, ev := range auditEvents(t, e.st, id) {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// scriptedUploads are three uploads for the fake to hand out in order.
func scriptedUploads() []procore.Upload {
	return []procore.Upload{
		{UUID: "u1", URL: "https://storage.example.com/bucket", Fields: map[string]string{"policy": "p1", "key": "uploads/u1"}},
		{UUID: "u2", URL: "https://storage.example.com/bucket", Fields: map[string]string{"policy": "p2", "key": "uploads/u2"}},
		{UUID: "u3", URL: "https://storage.example.com/bucket", Fields: map[string]string{"policy": "p3", "key": "uploads/u3"}},
	}
}

func TestRunSubmit_PushesAttachmentsThenPostsWithUploadIDs(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501, Number: "214"}
	e.fp.Uploads = scriptedUploads()
	d, bodies := e.storedPhotos(t, 2)
	e.submit(t, d)
	ctx := context.Background()

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}

	var creates, order []string
	for _, c := range e.fp.Calls() {
		switch c.Method {
		case "CreateUpload":
			i := len(creates)
			creates = append(creates, c.Upload.ResponseFilename)
			if c.AccessToken != "tok" || c.CompanyID != testCompany || c.ProjectID != testProject {
				t.Errorf("CreateUpload %d = %+v", i, c)
			}
			if *c.Upload != (procore.UploadCreate{ResponseFilename: d.Attachments[i].Filename, ResponseContentType: "image/jpeg"}) {
				t.Errorf("CreateUpload %d body = %+v", i, *c.Upload)
			}
			order = append(order, c.Method)
		case "PostUpload", "CreateRFI":
			order = append(order, c.Method)
		}
	}
	if want := []string{"CreateUpload", "PostUpload", "CreateUpload", "PostUpload", "CreateRFI"}; !reflect.DeepEqual(order, want) {
		t.Errorf("call order = %v, want %v", order, want)
	}
	if len(e.fp.Posted) != 2 {
		t.Fatalf("posted %d uploads, want 2", len(e.fp.Posted))
	}
	for i, p := range e.fp.Posted {
		up := e.fp.Uploads[i]
		if p.URL != up.URL || !reflect.DeepEqual(p.Fields, up.Fields) || p.Filename != d.Attachments[i].Filename ||
			p.Bytes != len(bodies[i]) || p.HasAuthHeader {
			t.Errorf("posted %d = %+v", i, p)
		}
		if want := []string{"key", "policy", "file"}; !reflect.DeepEqual(p.FieldOrder, want) {
			t.Errorf("posted %d field order = %v, want %v", i, p.FieldOrder, want)
		}
	}

	posted := e.postedRFI()
	if posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"u1", "u2"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}

	for i, a := range e.draftAttachments(t, d.ID) {
		if a.State != AttachmentUploaded || a.ProcoreUploadID != fmt.Sprintf("u%d", i+1) || !a.UploadedAt.Equal(e.clock.Now()) {
			t.Errorf("attachment %d = state %q upload %q at %s", i, a.State, a.ProcoreUploadID, a.UploadedAt)
		}
	}

	ups := auditsOfKind(t, e, d.ID, AuditUploaded)
	if len(ups) != 2 {
		t.Fatalf("%d uploaded audit events, want 2", len(ups))
	}
	for i, ev := range ups {
		var p struct {
			AttachmentID    uuid.UUID `json:"attachment_id"`
			ProcoreUploadID string    `json:"procore_upload_id"`
			Bytes           int64     `json:"bytes"`
			DurationMS      *int      `json:"duration_ms"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("uploaded payload %s: %v", ev.Payload, err)
		}
		if p.AttachmentID != d.Attachments[i].ID || p.ProcoreUploadID != fmt.Sprintf("u%d", i+1) ||
			p.Bytes != int64(len(bodies[i])) || p.DurationMS == nil {
			t.Errorf("uploaded audit %d = %s", i, ev.Payload)
		}
	}

	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Errorf("objects left after submit: %v", keys)
	}

	var attempt attemptAudit
	if err := json.Unmarshal(lastAudit(t, e, d.ID, AuditAttempt).Payload, &attempt); err != nil {
		t.Fatalf("attempt payload: %v", err)
	}
	if !reflect.DeepEqual(attempt.UploadIDs, []string{"u1", "u2"}) {
		t.Errorf("attempt upload_ids = %v", attempt.UploadIDs)
	}
	var req struct {
		RFI struct {
			Question struct {
				UploadIDs []string `json:"upload_ids"`
			} `json:"question"`
		} `json:"rfi"`
	}
	ev := lastAudit(t, e, d.ID, AuditSubmitted)
	if err := json.Unmarshal(ev.ProcoreRequest, &req); err != nil || !reflect.DeepEqual(req.RFI.Question.UploadIDs, []string{"u1", "u2"}) {
		t.Errorf("submitted procore_request = %s (%v)", ev.ProcoreRequest, err)
	}
}

func TestRunSubmit_ReusesRecentUploadIDs(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	e.fp.Uploads = scriptedUploads()
	d, _ := e.storedPhotos(t, 2)
	for i, a := range d.Attachments {
		e.setUploaded(t, a.ID, fmt.Sprintf("old%d", i+1), e.clock.Now().Add(-time.Hour))
	}
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n, m := e.fp.CallCount("CreateUpload"), e.fp.CallCount("PostUpload"); n != 0 || m != 0 {
		t.Errorf("CreateUpload %d PostUpload %d, want none", n, m)
	}
	if posted := e.postedRFI(); posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"old1", "old2"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted {
		t.Errorf("status = %q", got.Status)
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Errorf("objects left after submit: %v", keys)
	}
}

func TestRunSubmit_RepushesStaleUploadIDs(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	e.fp.Uploads = scriptedUploads()
	d, _ := e.storedPhotos(t, 2)
	for i, a := range d.Attachments {
		e.setUploaded(t, a.ID, fmt.Sprintf("old%d", i+1), e.clock.Now().Add(-6*24*time.Hour))
	}
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if n, m := e.fp.CallCount("CreateUpload"), e.fp.CallCount("PostUpload"); n != 2 || m != 2 {
		t.Errorf("CreateUpload %d PostUpload %d, want 2 each", n, m)
	}
	if posted := e.postedRFI(); posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"u1", "u2"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}
	for i, a := range e.draftAttachments(t, d.ID) {
		if a.State != AttachmentUploaded || a.ProcoreUploadID != fmt.Sprintf("u%d", i+1) || !a.UploadedAt.Equal(e.clock.Now()) {
			t.Errorf("attachment %d = state %q upload %q at %s", i, a.State, a.ProcoreUploadID, a.UploadedAt)
		}
	}
}

// photoFailed asserts a definitive photo failure on the first attachment:
// it is failed with code and message, the second is untouched, the draft
// failed with procore_validation and the message on the attachments field,
// no RFI was posted, no attempt was marked and both objects are kept.
func photoFailed(t *testing.T, e *draftsEnv, d Draft, code, message string) {
	t.Helper()
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodeProcoreValidation || got.Error.Message != message {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if u := flagFor(t, got, AttachmentsField); u.Reason != ReasonProcoreValidation || u.Message != message || !u.Blocking {
		t.Errorf("attachments flag = %+v", u)
	}
	atts := e.draftAttachments(t, d.ID)
	if a := atts[0]; a.State != AttachmentFailed || a.Error == nil || *a.Error != (DraftError{Code: code, Message: message}) {
		t.Errorf("first attachment = state %q error %+v", a.State, a.Error)
	}
	if a := atts[1]; a.State != AttachmentStored || a.Error != nil {
		t.Errorf("second attachment = state %q error %+v, want stored", a.State, a.Error)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times", n)
	}
	if evs := auditsOfKind(t, e, d.ID, AuditAttempt); len(evs) != 0 {
		t.Errorf("%d attempt markers, want none", len(evs))
	}
	lastAudit(t, e, d.ID, AuditFailed)
	if keys := e.blobs.Keys(); len(keys) != 2 {
		t.Errorf("objects = %v, want both kept", keys)
	}
}

func TestRunSubmit_PushRefused_FailsDraftWithoutPosting(t *testing.T) {
	for name, script := range map[string]func(e *draftsEnv){
		"storage 403": func(e *draftsEnv) { e.fp.PostErr = &procore.APIError{Status: 403, Body: "<Error>AccessDenied</Error>"} },
		"create 422": func(e *draftsEnv) {
			e.fp.Err["CreateUpload"] = &procore.ValidationError{Fields: map[string][]string{"response_filename": {"is invalid"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newDraftsEnv(t)
			e.fp.Uploads = scriptedUploads()
			script(e)
			d, _ := e.storedPhotos(t, 2)
			e.submit(t, d)

			if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
				t.Fatalf("RunSubmit = %v, want nil (not retried)", err)
			}
			if n := e.fp.CallCount("CreateUpload"); n != 1 {
				t.Errorf("CreateUpload called %d times, want 1 (stop at the refusal)", n)
			}
			photoFailed(t, e, d, AttachmentErrProcoreRefused,
				fmt.Sprintf("Photo %s could not be sent to Procore. Remove it and submit again.", d.Attachments[0].Filename))
		})
	}
}

func TestRunSubmit_ObjectMissing_FailsExpired(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Uploads = scriptedUploads()
	d, _ := e.storedPhotos(t, 2)
	e.submit(t, d)
	ctx := context.Background()
	if err := e.blobs.Delete(ctx, d.Attachments[0].ObjectKey); err != nil {
		t.Fatalf("delete object: %v", err)
	}

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil (not retried)", err)
	}
	if n, m := e.fp.CallCount("CreateUpload"), e.fp.CallCount("PostUpload"); n != 0 || m != 0 {
		t.Errorf("CreateUpload %d PostUpload %d, want none", n, m)
	}
	got := e.mustGet(t, d.ID)
	msg := fmt.Sprintf("Photo %s expired on the server. Remove it and submit again.", d.Attachments[0].Filename)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodeProcoreValidation || got.Error.Message != msg {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if u := flagFor(t, got, AttachmentsField); u.Message != msg {
		t.Errorf("attachments flag = %+v", u)
	}
	atts := e.draftAttachments(t, d.ID)
	if a := atts[0]; a.State != AttachmentFailed || a.Error == nil || *a.Error != (DraftError{Code: AttachmentErrExpired, Message: msg}) {
		t.Errorf("first attachment = state %q error %+v", a.State, a.Error)
	}
	if atts[1].State != AttachmentStored {
		t.Errorf("second attachment = %q, want stored", atts[1].State)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times", n)
	}
	if keys := e.blobs.Keys(); len(keys) != 1 || keys[0] != d.Attachments[1].ObjectKey {
		t.Errorf("objects = %v, want the second kept", keys)
	}
}

func TestRunSubmit_PushTransient_RetriesWithoutUnknownOutcome(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Uploads = scriptedUploads()
	e.fp.PostErr = fmt.Errorf("post upload: %w", procore.ErrUnavailable)
	d, _ := e.storedPhotos(t, 2)
	e.submit(t, d)
	ctx := context.Background()

	if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("RunSubmit = %v, want ErrUnavailable for River to retry", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitting || got.Error != nil {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if n := e.fp.CallCount("CreateRFI"); n != 0 {
		t.Errorf("CreateRFI called %d times", n)
	}
	if evs := auditsOfKind(t, e, d.ID, AuditAttempt); len(evs) != 0 {
		t.Errorf("%d attempt markers, want none", len(evs))
	}
	var r failedAudit
	if err := json.Unmarshal(lastAudit(t, e, d.ID, AuditFailed).ProcoreResponse, &r); err != nil || !r.BeforePost || r.UnknownOutcome {
		t.Errorf("failed audit = %+v (%v), want before_post", r, err)
	}
	for i, a := range e.draftAttachments(t, d.ID) {
		if a.State != AttachmentStored {
			t.Errorf("attachment %d = %q, want stored", i, a.State)
		}
	}
	prior, err := e.drafts.priorAttempts(ctx, d.ID)
	if err != nil || prior.unknown {
		t.Errorf("prior attempts = %+v (%v), want no unknown outcome", prior, err)
	}

	// The retry pushes and posts without looking for an earlier RFI.
	e.fp.PostErr = nil
	e.fp.Created = procore.RFICreated{ID: 501}
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("ListRFIs"); n != 0 {
		t.Errorf("ListRFIs called %d times, want 0", n)
	}
	if posted := e.postedRFI(); posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"u2", "u3"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}
}

func TestRunSubmit_PushInterruptedBeforeStore_PushesAgain(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	e.fp.Uploads = scriptedUploads()
	d, _ := e.storedPhotos(t, 2)
	e.submit(t, d)

	// The first storage POST succeeds, then the job's context ends before
	// the upload id is stored.
	ctx, cancel := context.WithCancel(context.Background())
	e.fp.OnPostUpload = cancel
	if err := e.drafts.RunSubmit(ctx, d.ID); err == nil {
		t.Fatal("RunSubmit = nil, want an error for River to retry")
	}
	if n := e.fp.CallCount("PostUpload"); n != 1 {
		t.Fatalf("PostUpload called %d times, want 1", n)
	}
	for i, a := range e.draftAttachments(t, d.ID) {
		if a.State != AttachmentStored || a.ProcoreUploadID != "" {
			t.Errorf("attachment %d = state %q upload %q, want stored", i, a.State, a.ProcoreUploadID)
		}
	}

	e.fp.OnPostUpload = nil
	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("CreateUpload"); n != 3 {
		t.Errorf("CreateUpload called %d times, want 3", n)
	}
	if posted := e.postedRFI(); posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"u2", "u3"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted {
		t.Errorf("status = %q", got.Status)
	}
}

func TestRunSubmit_PushReauth_MarksFailed(t *testing.T) {
	for name, err := range map[string]error{
		"unauthorized": fmt.Errorf("create upload: %w", procore.ErrUnauthorized),
		"reauth":       fmt.Errorf("refresh: %w", auth.ErrReauthRequired),
	} {
		t.Run(name, func(t *testing.T) {
			e := newDraftsEnv(t)
			e.fp.UploadErr = err
			d, _ := e.storedPhotos(t, 2)
			e.submit(t, d)

			if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
				t.Fatalf("RunSubmit = %v, want nil", err)
			}
			got := e.mustGet(t, d.ID)
			if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodeReauthRequired {
				t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
			}
			if n := e.fp.CallCount("CreateRFI"); n != 0 {
				t.Errorf("CreateRFI called %d times", n)
			}
			for i, a := range e.draftAttachments(t, d.ID) {
				if a.State != AttachmentStored || a.Error != nil {
					t.Errorf("attachment %d = state %q error %+v, want stored", i, a.State, a.Error)
				}
			}
			if keys := e.blobs.Keys(); len(keys) != 2 {
				t.Errorf("objects = %v, want both kept", keys)
			}
		})
	}
}

func TestRunSubmit_PushPermissionDenied_MarksFailed(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["CreateUpload"] = fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403})
	d, _ := e.storedPhotos(t, 2)
	e.submit(t, d)

	if err := e.drafts.RunSubmit(context.Background(), d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil", err)
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodePermissionDenied {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if a := e.draftAttachments(t, d.ID)[0]; a.State != AttachmentStored {
		t.Errorf("first attachment = %q, want stored", a.State)
	}
}

func TestRunSubmit_RecoveredRFI_DeletesObjectsWithoutRepush(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Uploads = scriptedUploads()
	e.fp.Created = procore.RFICreated{ID: 999}
	d, _ := e.storedPhotos(t, 2)
	// An earlier attempt pushed both photos, long enough ago that a push
	// now would create new uploads, and posted; its outcome is unknown and
	// its RFI is in Procore.
	for i, a := range d.Attachments {
		e.setUploaded(t, a.ID, fmt.Sprintf("old%d", i+1), e.clock.Now().Add(-6*24*time.Hour))
	}
	e.submit(t, d)
	ctx := context.Background()
	if err := e.drafts.writeAttempt(ctx, e.mustGet(t, d.ID), 1, "Twisted column", e.clock.Now(), "old1", "old2"); err != nil {
		t.Fatalf("writeAttempt: %v", err)
	}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	e.clock.Advance(2 * time.Minute)

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
	for _, m := range []string{"CreateUpload", "PostUpload", "CreateRFI"} {
		if n := e.fp.CallCount(m); n != 0 {
			t.Errorf("%s called %d times, want 0", m, n)
		}
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Errorf("objects left after recovery: %v", keys)
	}
}

// uncoveredRecovery sets up a draft whose earlier attempt posted with photo
// a (upload u1) and has an unknown outcome, its RFI in Procore, while photo
// b was stored afterwards and never pushed.
func uncoveredRecovery(t *testing.T, e *draftsEnv) Draft {
	t.Helper()
	e.fp.Uploads = scriptedUploads()
	e.fp.Created = procore.RFICreated{ID: 999}
	d, _ := e.storedPhotos(t, 2)
	e.setUploaded(t, d.Attachments[0].ID, "u1", e.clock.Now())
	e.submit(t, d)
	if err := e.drafts.writeAttempt(context.Background(), e.mustGet(t, d.ID), 1, "Twisted column", e.clock.Now(), "u1"); err != nil {
		t.Fatalf("writeAttempt: %v", err)
	}
	e.fp.RFIs = []procore.RFI{{ID: 501, Number: "214", Subject: "Twisted column", CreatedBy: pat, CreatedAt: e.clock.Now().Format(time.RFC3339)}}
	e.clock.Advance(2 * time.Minute)
	return e.mustGet(t, d.ID)
}

func TestRunSubmit_RecoveredRFI_UncoveredPhoto_RefusesWithoutPosting(t *testing.T) {
	e := newDraftsEnv(t)
	d := uncoveredRecovery(t, e)
	ctx := context.Background()

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("RunSubmit = %v, want nil (not retried)", err)
	}
	msg := fmt.Sprintf("An earlier attempt created this RFI without photo %s. Remove the photo and submit again, or add it in Procore.", d.Attachments[1].Filename)
	got := e.mustGet(t, d.ID)
	if got.Status != StatusFailed || got.Error == nil || got.Error.Code != CodeProcoreValidation || got.Error.Message != msg {
		t.Fatalf("draft = status %q error %+v", got.Status, got.Error)
	}
	if u := flagFor(t, got, AttachmentsField); u.Message != msg || u.Reason != ReasonProcoreValidation {
		t.Errorf("attachments flag = %+v", u)
	}
	for _, m := range []string{"CreateUpload", "PostUpload", "CreateRFI"} {
		if n := e.fp.CallCount(m); n != 0 {
			t.Errorf("%s called %d times, want 0", m, n)
		}
	}
	if keys := e.blobs.Keys(); len(keys) != 2 {
		t.Errorf("objects = %v, want both kept", keys)
	}
	if evs := auditsOfKind(t, e, d.ID, AuditAttempt); len(evs) != 1 {
		t.Errorf("%d attempt markers, want only the earlier one", len(evs))
	}
	prior, err := e.drafts.priorAttempts(ctx, d.ID)
	if err != nil || !prior.unknown {
		t.Errorf("prior attempts = %+v (%v), want the run still unknown", prior, err)
	}
	if atts := e.draftAttachments(t, d.ID); atts[0].State != AttachmentUploaded || atts[1].State != AttachmentStored {
		t.Errorf("attachments = %q, %q; want uploaded, stored", atts[0].State, atts[1].State)
	}
}

func TestRunSubmit_RecoveredRFI_AfterUncoveredPhotoRemoved_Adopts(t *testing.T) {
	e := newDraftsEnv(t)
	d := uncoveredRecovery(t, e)
	ctx := context.Background()
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := e.drafts.DeleteAttachment(ctx, e.user, d.ID, d.Attachments[1].ID); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	e.submit(t, d)

	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := e.mustGet(t, d.ID); got.Status != StatusSubmitted || got.Result == nil || got.Result.ProcoreID != 501 {
		t.Fatalf("draft = status %q result %+v", got.Status, got.Result)
	}
	for _, m := range []string{"CreateUpload", "PostUpload", "CreateRFI"} {
		if n := e.fp.CallCount(m); n != 0 {
			t.Errorf("%s called %d times, want 0", m, n)
		}
	}
	if keys := e.blobs.Keys(); len(keys) != 0 {
		t.Errorf("objects left after recovery: %v", keys)
	}
}

func TestRunSubmit_PartialPush_RetryReusesFirstAndPushesSecond(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Created = procore.RFICreated{ID: 501}
	e.fp.Uploads = scriptedUploads()
	d, _ := e.storedPhotos(t, 2)
	e.submit(t, d)
	ctx := context.Background()

	// Photo a goes up; creating photo b's upload then fails transiently.
	e.fp.OnPostUpload = func() { e.fp.UploadErr = fmt.Errorf("create upload: %w", procore.ErrUnavailable) }
	if err := e.drafts.RunSubmit(ctx, d.ID); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("first run = %v, want ErrUnavailable", err)
	}
	if atts := e.draftAttachments(t, d.ID); atts[0].State != AttachmentUploaded || atts[0].ProcoreUploadID != "u1" || atts[1].State != AttachmentStored {
		t.Fatalf("after first run: a %q %q, b %q", atts[0].State, atts[0].ProcoreUploadID, atts[1].State)
	}
	creates := e.fp.CallCount("CreateUpload")

	e.fp.OnPostUpload, e.fp.UploadErr = nil, nil
	if err := e.drafts.RunSubmit(ctx, d.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.fp.CallCount("CreateUpload") - creates; n != 1 {
		t.Errorf("retry created %d uploads, want 1 (photo b only)", n)
	}
	if c := e.fp.Calls(); c[len(c)-3].Method != "CreateUpload" || c[len(c)-3].Upload.ResponseFilename != d.Attachments[1].Filename {
		t.Errorf("retry's upload was for %+v, want photo b", c[len(c)-3].Upload)
	}
	if posted := e.postedRFI(); posted == nil || !reflect.DeepEqual(posted.Question.UploadIDs, []string{"u1", "u2"}) {
		t.Fatalf("posted rfi = %+v", posted)
	}
}

func TestRunSubmit_StorageErrorCodes_TransientOrRefused(t *testing.T) {
	for _, tc := range []struct {
		code      string
		status    int
		transient bool
	}{
		{"RequestTimeout", 400, true},
		{"RequestTimeTooSkewed", 403, true},
		{"ExpiredToken", 400, true},
		{"SlowDown", 503, true},
		{"AccessDenied", 403, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := newDraftsEnv(t)
			e.fp.Uploads = scriptedUploads()
			e.fp.PostErr = &procore.APIError{Status: tc.status, Body: "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>" + tc.code + "</Code><Message>m</Message></Error>"}
			d, _ := e.storedPhotos(t, 2)
			e.submit(t, d)

			err := e.drafts.RunSubmit(context.Background(), d.ID)
			got := e.mustGet(t, d.ID)
			first := e.draftAttachments(t, d.ID)[0]
			if tc.transient {
				if err == nil || got.Status != StatusSubmitting || first.State != AttachmentStored {
					t.Errorf("RunSubmit = %v, draft %q, photo %q; want a retry with the photo stored", err, got.Status, first.State)
				}
				return
			}
			if err != nil || got.Status != StatusFailed || first.State != AttachmentFailed || first.Error == nil || first.Error.Code != AttachmentErrProcoreRefused {
				t.Errorf("RunSubmit = %v, draft %q, photo %q %+v; want refused", err, got.Status, first.State, first.Error)
			}
		})
	}
}
