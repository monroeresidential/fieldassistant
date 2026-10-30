package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// photoBody is n bytes that start with the JPEG marker.
func photoBody(n int) []byte {
	b := bytes.Repeat([]byte{0x42}, n)
	copy(b, []byte{0xFF, 0xD8, 0xFF, 0xE0})
	return b
}

// manifest returns n manifest entries with fresh ids.
func manifest(n int) []AttachmentManifestItem {
	out := make([]AttachmentManifestItem, n)
	for i := range out {
		out[i] = AttachmentManifestItem{
			Id: uuid.New(), Filename: "IMG_" + strconv.Itoa(i+1) + ".jpg",
			ContentType: Imagejpeg, Bytes: 1000,
		}
	}
	return out
}

// createDraftWithPhotos posts a new draft whose manifest lists n photos and
// fails the test unless it answers 200.
func createDraftWithPhotos(t *testing.T, srv *httptest.Server, token string, n int) DraftResponse {
	t.Helper()
	body := createDraftBody(uuid.New())
	items := manifest(n)
	body.Attachments = &items
	res, b := draftCall(t, srv, http.MethodPost, "/v1/drafts", token, draftCompany, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create draft: status %d; body %s", res.StatusCode, b)
	}
	return decode[DraftResponse](t, b)
}

func attachmentPath(draftID, attachmentID uuid.UUID) string {
	return "/v1/drafts/" + draftID.String() + "/attachments/" + attachmentID.String()
}

// sendAttachment sends method to the attachment's path with body as
// contentType. It reports failures instead of failing the test, so
// goroutines can call it.
func sendAttachment(srv *httptest.Server, method, token string, draftID, attachmentID uuid.UUID, contentType string, body io.Reader) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+attachmentPath(draftID, attachmentID), body)
	if err != nil {
		return nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Company-Id", strconv.FormatInt(draftCompany, 10))
	res, err := noRedirectClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return res, b, err
}

// putAttachment PUTs body as the attachment's bytes.
func putAttachment(t *testing.T, srv *httptest.Server, token string, draftID, attachmentID uuid.UUID, body []byte) (*http.Response, []byte) {
	t.Helper()
	res, b, err := sendAttachment(srv, http.MethodPut, token, draftID, attachmentID, "image/jpeg", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	return res, b
}

// deleteAttachment DELETEs the attachment.
func deleteAttachment(t *testing.T, srv *httptest.Server, token string, draftID, attachmentID uuid.UUID) (*http.Response, []byte) {
	t.Helper()
	res, b, err := sendAttachment(srv, http.MethodDelete, token, draftID, attachmentID, "", nil)
	if err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	return res, b
}

// getDraft fetches the draft and fails the test unless it answers 200.
func getDraft(t *testing.T, srv *httptest.Server, token string, id uuid.UUID) DraftResponse {
	t.Helper()
	res, b := draftCall(t, srv, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get draft: status %d; body %s", res.StatusCode, b)
	}
	return decode[DraftResponse](t, b)
}

// wantMessage asserts res is a status/code Error whose message is msg.
func wantMessage(t *testing.T, res *http.Response, b []byte, status int, code, msg string) {
	t.Helper()
	wantError(t, res, b, status, code)
	if e := decode[Error](t, b); e.Message != msg {
		t.Fatalf("message %q, want %q", e.Message, msg)
	}
}

func TestCreateDraft_WithManifest_ReturnsPendingAttachments(t *testing.T) {
	ts, token := newDraftServer(t)
	body := createDraftBody(uuid.New())
	items := manifest(2)
	body.Attachments = &items

	res, b := draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	d := decode[DraftResponse](t, b)
	if len(d.Attachments) != 2 {
		t.Fatalf("attachments = %+v, want 2", d.Attachments)
	}
	for i, a := range d.Attachments {
		if a.Id != items[i].Id || a.Filename != items[i].Filename || a.Bytes != 1000 ||
			a.State != AttachmentStatePending || a.ProcoreUploadId != nil || a.Error != nil {
			t.Errorf("attachment %d = %+v, want pending %s", i, a, items[i].Filename)
		}
	}
	if got := getDraft(t, ts.Server, token, body.Id).Attachments; !reflect.DeepEqual(got, d.Attachments) {
		t.Errorf("GET attachments = %+v, want %+v", got, d.Attachments)
	}

	// A draft without a manifest answers an empty array, not null.
	res, b = draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, createDraftBody(uuid.New()))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	var raw struct {
		Attachments *json.RawMessage `json:"attachments"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	if raw.Attachments == nil || string(*raw.Attachments) != "[]" {
		t.Errorf("attachments = %v, want []", raw.Attachments)
	}

	// More than ten photos is refused.
	body = createDraftBody(uuid.New())
	items = manifest(11)
	body.Attachments = &items
	res, b = draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, body)
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "at most 10 photos")

	// The contract's manifest rules are checked, since nothing validates
	// the schema: a repeated id would otherwise fail the insert.
	body = createDraftBody(uuid.New())
	items = manifest(2)
	items[1].Id = items[0].Id
	body.Attachments = &items
	res, b = draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, body)
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "photo ids must be unique")
	items = manifest(1)
	items[0].ContentType = "image/png"
	body.Attachments = &items
	res, b = draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, body)
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "photo content_type must be image/jpeg")
}

func TestPutAttachment_StoresAndReturns200(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)
	att := d.Attachments[0]
	photo := photoBody(4096)

	res, b := putAttachment(t, ts.Server, token, d.Id, att.Id, photo)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	got := decode[Attachment](t, b)
	if got.Id != att.Id || got.Filename != att.Filename || got.Bytes != int64(len(photo)) || got.State != AttachmentStateStored {
		t.Fatalf("attachment = %+v, want stored with %d bytes", got, len(photo))
	}
	key := blob.Key(draftCompany, d.Id, att.Id)
	if keys := ts.Blobs.Keys(); !reflect.DeepEqual(keys, []string{key}) {
		t.Fatalf("keys = %v, want [%s]", keys, key)
	}
	if after := getDraft(t, ts.Server, token, d.Id).Attachments; len(after) != 1 || after[0].State != AttachmentStateStored {
		t.Errorf("draft attachments = %+v, want one stored", after)
	}
}

func TestPutAttachment_Repeat_200_SameObject(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)
	att := d.Attachments[0]
	first := photoBody(2048)

	res, b := putAttachment(t, ts.Server, token, d.Id, att.Id, first)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first put: status %d; body %s", res.StatusCode, b)
	}
	stored := decode[Attachment](t, b)
	res, b = putAttachment(t, ts.Server, token, d.Id, att.Id, photoBody(999))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("repeat put: status %d; body %s", res.StatusCode, b)
	}
	if again := decode[Attachment](t, b); !reflect.DeepEqual(again, stored) {
		t.Errorf("repeat = %+v, want %+v", again, stored)
	}
	key := blob.Key(draftCompany, d.Id, att.Id)
	if keys := ts.Blobs.Keys(); !reflect.DeepEqual(keys, []string{key}) {
		t.Fatalf("keys = %v, want [%s]", keys, key)
	}
	rc, _, err := ts.Blobs.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer rc.Close()
	if obj, _ := io.ReadAll(rc); !bytes.Equal(obj, first) {
		t.Errorf("object holds %d bytes, want the first body's %d", len(obj), len(first))
	}
}

func TestPutAttachment_NotJPEG_400(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)

	res, b := putAttachment(t, ts.Server, token, d.Id, d.Attachments[0].Id, []byte("this is not a photo"))
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "photo must be a JPEG under 20 MiB")

	// JPEG bytes sent as another type are refused too.
	res, b, err := sendAttachment(ts.Server, http.MethodPut, token, d.Id, d.Attachments[0].Id, "image/png", bytes.NewReader(photoBody(100)))
	if err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "photo must be a JPEG under 20 MiB")
	if keys := ts.Blobs.Keys(); len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}
	if after := getDraft(t, ts.Server, token, d.Id).Attachments; after[0].State != AttachmentStatePending {
		t.Errorf("attachment = %+v, want pending", after[0])
	}
}

func TestPutAttachment_OverLimit_400_NothingStored(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)

	// Hiding the reader's length makes the client send the body chunked,
	// with no Content-Length.
	body := struct{ io.Reader }{bytes.NewReader(photoBody(20<<20 + 1))}
	res, b, err := sendAttachment(ts.Server, http.MethodPut, token, d.Id, d.Attachments[0].Id, "image/jpeg", body)
	if err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "photo must be a JPEG under 20 MiB")
	if keys := ts.Blobs.Keys(); len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}
	if after := getDraft(t, ts.Server, token, d.Id).Attachments; after[0].State != AttachmentStatePending {
		t.Errorf("attachment = %+v, want pending", after[0])
	}
}

func TestPutAttachment_Concurrent_OneRowOneObject(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)
	att := d.Attachments[0]
	photo := photoBody(8192)

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, b, err := sendAttachment(ts.Server, http.MethodPut, token, d.Id, att.Id, "image/jpeg", bytes.NewReader(photo))
			if err != nil {
				t.Errorf("put %d: %v", i, err)
				return
			}
			if statuses[i] = res.StatusCode; res.StatusCode != http.StatusOK {
				t.Errorf("put %d: status %d; body %s", i, res.StatusCode, b)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	after := getDraft(t, ts.Server, token, d.Id).Attachments
	if len(after) != 1 || after[0].Id != att.Id || after[0].State != AttachmentStateStored || after[0].Bytes != int64(len(photo)) {
		t.Fatalf("draft attachments = %+v, want one stored", after)
	}
	key := blob.Key(draftCompany, d.Id, att.Id)
	if keys := ts.Blobs.Keys(); !reflect.DeepEqual(keys, []string{key}) {
		t.Fatalf("keys = %v, want [%s]", keys, key)
	}
}

func TestPutAttachment_Locked_400(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)
	if err := ts.Store.Q.SetDraftStatus(context.Background(), db.SetDraftStatusParams{
		ID: pgtype.UUID{Bytes: d.Id, Valid: true}, Status: "submitting",
	}); err != nil {
		t.Fatalf("set status: %v", err)
	}

	res, b := putAttachment(t, ts.Server, token, d.Id, d.Attachments[0].Id, photoBody(100))
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "draft is submitted or being submitted")
	res, b = deleteAttachment(t, ts.Server, token, d.Id, d.Attachments[0].Id)
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "draft is submitted or being submitted")
	if keys := ts.Blobs.Keys(); len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}
}

func TestPutAttachment_OtherUser_404(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 1)
	ts.Procore.With(func(f *fakeProcore) {
		f.Users[11] = procore.Me{ID: 11, Login: "ben@example.com", Name: "Ben West"}
	})
	other := signInAs(t, ts.Server, "device-2", "11").AccessToken

	res, b := putAttachment(t, ts.Server, other, d.Id, d.Attachments[0].Id, photoBody(100))
	wantMessage(t, res, b, http.StatusNotFound, "not_found", "draft not found")
	res, b = deleteAttachment(t, ts.Server, other, d.Id, d.Attachments[0].Id)
	wantMessage(t, res, b, http.StatusNotFound, "not_found", "draft not found")

	// The owner's draft has no attachment with an unknown id.
	res, b = putAttachment(t, ts.Server, token, d.Id, uuid.New(), photoBody(100))
	wantMessage(t, res, b, http.StatusNotFound, "not_found", "attachment is not on this draft")
	if keys := ts.Blobs.Keys(); len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}
}

func TestDeleteAttachment_204(t *testing.T) {
	ts, token := newDraftServer(t)
	d := createDraftWithPhotos(t, ts.Server, token, 2)
	gone, kept := d.Attachments[0], d.Attachments[1]
	if res, b := putAttachment(t, ts.Server, token, d.Id, gone.Id, photoBody(100)); res.StatusCode != http.StatusOK {
		t.Fatalf("put: status %d; body %s", res.StatusCode, b)
	}

	res, b := deleteAttachment(t, ts.Server, token, d.Id, gone.Id)
	if res.StatusCode != http.StatusNoContent || len(b) != 0 {
		t.Fatalf("delete: status %d; body %q", res.StatusCode, b)
	}
	if after := getDraft(t, ts.Server, token, d.Id).Attachments; len(after) != 1 || after[0].Id != kept.Id {
		t.Errorf("draft attachments = %+v, want only %s", after, kept.Id)
	}
	if keys := ts.Blobs.Keys(); len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}

	res, b = deleteAttachment(t, ts.Server, token, d.Id, gone.Id)
	wantMessage(t, res, b, http.StatusNotFound, "not_found", "attachment is not on this draft")
}

func TestSubmit_PendingAttachment_400Message(t *testing.T) {
	ts, token := newSubmitServer(t)

	one := createDraftWithPhotos(t, ts.Server, token, 1)
	res, b := submitCall(t, ts.Server, token, one.Id, one.Id.String())
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "1 photo has not finished uploading")

	two := createDraftWithPhotos(t, ts.Server, token, 2)
	res, b = submitCall(t, ts.Server, token, two.Id, two.Id.String())
	wantMessage(t, res, b, http.StatusBadRequest, "bad_request", "2 photos have not finished uploading")

	if got := getDraft(t, ts.Server, token, two.Id); got.Status != DraftResponseStatusExtracted {
		t.Errorf("draft status = %q, want extracted", got.Status)
	}
}
