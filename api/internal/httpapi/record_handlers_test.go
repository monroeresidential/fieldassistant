package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

const (
	recordCompany = int64(7)
	recordProject = int64(376187)
	// recordRFIsPath is the Procore list path the mirror pulls for recordProject.
	recordRFIsPath = "/rest/v1.0/projects/376187/rfis"
)

// recordCall sends method path with X-Company-Id recordCompany, a bearer
// token, an optional JSON body and extra headers given as name/value pairs.
func recordCall(t *testing.T, srv *httptest.Server, method, path, bearer string, body any, headers ...string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Company-Id", strconv.FormatInt(recordCompany, 10))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, b
}

// getRecordPage calls GET /v1/projects/recordProject/records/rfi?since=since
// and fails the test unless it answers 200.
func getRecordPage(t *testing.T, srv *httptest.Server, bearer string, since int64, headers ...string) RecordPage {
	t.Helper()
	path := "/v1/projects/376187/records/rfi?since=" + strconv.FormatInt(since, 10)
	res, b := recordCall(t, srv, http.MethodGet, path, bearer, nil, headers...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("records: status %d; body %s", res.StatusCode, b)
	}
	return decode[RecordPage](t, b)
}

// attachmentID and answerAttachmentID are the ids of the question and answer
// attachments recordRFI puts on RFI id.
func attachmentID(id int64) int64       { return id*100 + 1 }
func answerAttachmentID(id int64) int64 { return id*100 + 2 }

// recordRFI is a Procore Show RFI body for id, updated at updated, whose
// question and answer each carry one attachment served from fileURL.
func recordRFI(id int64, subject string, updated time.Time, fileURL string) map[string]any {
	ts := updated.UTC().Format(time.RFC3339)
	return map[string]any{
		"id":          id,
		"number":      id - 1000, // Procore sends a JSON number
		"full_number": "RFI-" + strconv.FormatInt(id-1000, 10),
		"subject":     subject,
		"status":      "open",
		"draft":       false,
		"private":     false,
		"due_date":    "2026-11-01",
		"created_at":  ts,
		"updated_at":  ts,
		"created_by":  map[string]any{"id": 42, "name": "Pat Manager"},
		"assignees": []map[string]any{
			{"id": 11, "name": "Ben West"},
			{"id": 12, "name": "Ann Lee"},
		},
		"ball_in_courts":         []map[string]any{{"id": 11, "name": "Ben West"}},
		"distribution_list":      []map[string]any{},
		"location":               nil,
		"rfi_manager":            map[string]any{"id": 0, "name": ""},
		"specification_section":  map[string]any{"id": 5, "label": "", "number": "03 30 00", "description": "Cast-in-Place Concrete"},
		"cost_code":              map[string]any{"id": 6, "full_code": "01-100", "name": "General"},
		"cost_impact":            map[string]any{"status": "yes_known", "value": 1500},
		"drawing_ids":            []int64{},
		"responsible_contractor": nil,
		"questions": []map[string]any{{
			"id":              id * 10,
			"plain_text_body": "Which beam size at C4?",
			"question_date":   ts,
			"attachments": []map[string]any{{
				"id": attachmentID(id), "name": "Photo", "filename": "photo.jpg", "url": fileURL,
			}},
			"answers": []map[string]any{{
				"id":              id*10 + 1,
				"plain_text_body": "Use W12x26.",
				"answer_date":     ts,
				"attachments": []map[string]any{{
					"id": answerAttachmentID(id), "name": "Sketch", "filename": "", "url": fileURL,
				}},
			}},
		}},
	}
}

// newRecordServer starts a test server where company 7 is visible and the
// Procore user 42 can see RFIs 1001 and 1002 in recordProject, whose
// attachments are served as /files/photo.jpg. It returns the server, the fake
// and user 42's access token.
func newRecordServer(t *testing.T) (*httptest.Server, *fakeProcore, string) {
	t.Helper()
	srv, fp := newTestServer(t)
	fp.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: recordCompany, Name: "Monroe Residential"}}
		updated := f.Clock.now().Add(-time.Hour)
		fileURL := f.Server.URL + "/files/photo.jpg"
		f.RecordRFIs[testProcoreUser.ID] = []map[string]any{
			recordRFI(1001, "Beam size at C4", updated, fileURL),
			recordRFI(1002, "Slab edge detail", updated.Add(time.Minute), fileURL),
		}
		f.Files["photo.jpg"] = fakeFile{ContentType: "image/jpeg", Body: []byte("\xff\xd8jpeg-bytes")}
	})
	return srv, fp, signIn(t, srv, "device-1").AccessToken
}

func TestGetRecords_FirstPageAndCursor(t *testing.T) {
	srv, _, token := newRecordServer(t)

	page := getRecordPage(t, srv, token, 0)
	if page.Pull.Status != "pulled" || page.Pull.Kind != "rfi" || page.Pull.PulledAt == nil || page.Pull.Code != nil {
		t.Fatalf("pull %+v, want pulled rfi with pulled_at", page.Pull)
	}
	if page.SyncedAt == nil {
		t.Fatal("synced_at is nil after a pull")
	}
	if len(page.Records) != 2 || len(page.DeletedIds) != 0 || page.HasMore {
		t.Fatalf("page %+v, want 2 records", page)
	}
	if page.NextSince != page.Records[1].ChangeSeq {
		t.Fatalf("next_since %d, want last change_seq %d", page.NextSince, page.Records[1].ChangeSeq)
	}
	byID := map[int64]RecordEnvelope{}
	for _, r := range page.Records {
		byID[r.Id] = r
	}
	env, ok := byID[1001]
	if !ok || env.Kind != "rfi" || env.Rfi == nil {
		t.Fatalf("record 1001 %+v, want an rfi projection", env)
	}
	rfi := *env.Rfi
	if rfi.Id != 1001 || rfi.Subject != "Beam size at C4" || rfi.Number != "1" || rfi.Status != "open" {
		t.Fatalf("rfi %+v", rfi)
	}
	if rfi.FullNumber == nil || *rfi.FullNumber != "RFI-1" {
		t.Fatalf("full_number %v, want RFI-1", rfi.FullNumber)
	}
	if len(rfi.Assignees) != 2 || rfi.Assignees[0].Name != "Ben West" || rfi.Assignees[1].Name != "Ann Lee" {
		t.Fatalf("assignees %+v", rfi.Assignees)
	}
	if rfi.DueDate == nil || rfi.DueDate.Format("2006-01-02") != "2026-11-01" {
		t.Fatalf("due_date %v, want 2026-11-01", rfi.DueDate)
	}
	if rfi.CreatedBy == nil || rfi.CreatedBy.Name != "Pat Manager" {
		t.Fatalf("created_by %+v", rfi.CreatedBy)
	}
	if rfi.Location != nil || rfi.RfiManager != nil || rfi.ResponsibleContractor != nil {
		t.Fatalf("null or id-0 refs not omitted: location %+v rfi_manager %+v contractor %+v", rfi.Location, rfi.RfiManager, rfi.ResponsibleContractor)
	}
	if rfi.SpecificationSection == nil || rfi.SpecificationSection.Name != "03 30 00 Cast-in-Place Concrete" {
		t.Fatalf("specification_section %+v", rfi.SpecificationSection)
	}
	if rfi.CostCode == nil || rfi.CostCode.Name != "01-100 General" {
		t.Fatalf("cost_code %+v", rfi.CostCode)
	}
	if rfi.CostImpact == nil || rfi.CostImpact.Value == nil || *rfi.CostImpact.Value != "1500" {
		t.Fatalf("cost_impact %+v", rfi.CostImpact)
	}
	if len(rfi.Questions) != 1 || len(rfi.Questions[0].Attachments) != 1 || len(rfi.Questions[0].Answers) != 1 {
		t.Fatalf("questions %+v", rfi.Questions)
	}
	att := rfi.Questions[0].Attachments[0]
	wantURL := "/v1/projects/376187/records/rfi/1001/attachments/" + strconv.FormatInt(attachmentID(1001), 10)
	if att.Url != wantURL || att.Name != "Photo" || att.Filename == nil || *att.Filename != "photo.jpg" {
		t.Fatalf("question attachment %+v, want url %s", att, wantURL)
	}
	ans := rfi.Questions[0].Answers[0]
	if ans.PlainTextBody != "Use W12x26." || len(ans.Attachments) != 1 || ans.Attachments[0].Filename != nil {
		t.Fatalf("answer %+v, want one attachment without filename", ans)
	}

	next := getRecordPage(t, srv, token, page.NextSince)
	if len(next.Records) != 0 || len(next.DeletedIds) != 0 || next.HasMore || next.NextSince != page.NextSince {
		t.Fatalf("page after cursor %+v, want empty at the same cursor", next)
	}
	if next.Pull.Status != "fresh" {
		t.Fatalf("second pull status %q, want fresh", next.Pull.Status)
	}
}

func TestGetRecords_NoCacheForcesPull(t *testing.T) {
	srv, fp, token := newRecordServer(t)

	getRecordPage(t, srv, token, 0)
	page := getRecordPage(t, srv, token, 0, "Cache-Control", "no-cache")
	if page.Pull.Status != "pulled" {
		t.Fatalf("forced pull status %q, want pulled", page.Pull.Status)
	}
	if n := fp.requestsTo(recordRFIsPath); n != 2 {
		t.Fatalf("Procore rfis requests %d, want 2", n)
	}
	// The first pull is full; the forced one lists only recent changes.
	var filters []string
	fp.With(func(f *fakeProcore) {
		for _, r := range f.Requests {
			if r.Path == recordRFIsPath {
				filters = append(filters, r.Query.Get("filters[updated_at]"))
			}
		}
	})
	if filters[0] != "" || filters[1] == "" {
		t.Fatalf("filters[updated_at] per pull %q, want none then a range", filters)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records %d, want 2", len(page.Records))
	}
}

func TestGetRecords_PermissionDeniedIsSoft(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	first := getRecordPage(t, srv, token, 0)
	if len(first.Records) != 2 {
		t.Fatalf("records before denial %d, want 2", len(first.Records))
	}
	fp.With(func(f *fakeProcore) { f.Forbidden["/projects/376187/rfis"] = true })

	// The client at its cursor is told both records are gone.
	page := getRecordPage(t, srv, token, first.NextSince, "Cache-Control", "no-cache")
	if len(page.Records) != 0 || len(page.DeletedIds) != 2 {
		t.Fatalf("page since cursor %+v, want 1001 and 1002 deleted", page)
	}
	// A fresh client gets no records.
	page = getRecordPage(t, srv, token, 0)
	if page.Pull.Status != "failed" || page.Pull.Code == nil || *page.Pull.Code != "procore_permission_denied" {
		t.Fatalf("pull %+v, want failed procore_permission_denied", page.Pull)
	}
	if page.Pull.Message == nil || *page.Pull.Message == "" {
		t.Fatal("failed pull has no message")
	}
	if len(page.Records) != 0 {
		t.Fatalf("page %+v, want no records", page)
	}
}

func TestGetRecords_OtherUserSeesOwnMirrorOnly(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	other := procore.Me{ID: 43, Login: "super@example.com", Name: "Sam Super"}
	fp.With(func(f *fakeProcore) {
		f.Users[other.ID] = other
		f.RecordRFIs[other.ID] = []map[string]any{
			recordRFI(2001, "Door hardware", f.Clock.now().Add(-time.Hour), f.Server.URL+"/files/photo.jpg"),
		}
	})
	otherToken := signInAs(t, srv, "device-2", "43").AccessToken

	ids := func(p RecordPage) []int64 {
		out := []int64{}
		for _, r := range p.Records {
			out = append(out, r.Id)
		}
		return out
	}
	if got := ids(getRecordPage(t, srv, token, 0)); len(got) != 2 || got[0] == 2001 || got[1] == 2001 {
		t.Fatalf("user 42 records %v, want 1001 and 1002 only", got)
	}
	if got := ids(getRecordPage(t, srv, otherToken, 0)); len(got) != 1 || got[0] != 2001 {
		t.Fatalf("user 43 records %v, want 2001 only", got)
	}
}

func TestSyncProject_FullSweepDeletes(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	first := getRecordPage(t, srv, token, 0)
	fp.With(func(f *fakeProcore) {
		f.RecordRFIs[testProcoreUser.ID] = f.RecordRFIs[testProcoreUser.ID][:1] // 1002 is gone
	})

	full := true
	res, b := recordCall(t, srv, http.MethodPost, "/v1/projects/376187/sync", token,
		SyncRequest{Kinds: []SyncRequestKinds{"rfi"}, Full: &full})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sync: status %d; body %s", res.StatusCode, b)
	}
	result := decode[SyncResult](t, b)
	if len(result.Pulls) != 1 || result.Pulls[0].Kind != "rfi" || result.Pulls[0].Status != "pulled" {
		t.Fatalf("pulls %+v, want one pulled rfi", result.Pulls)
	}

	page := getRecordPage(t, srv, token, first.NextSince)
	if len(page.DeletedIds) != 1 || page.DeletedIds[0] != 1002 || len(page.Records) != 0 {
		t.Fatalf("page %+v, want 1002 deleted", page)
	}
	if n := fp.requestsTo(recordRFIsPath); n != 2 {
		t.Fatalf("Procore rfis requests %d, want 2", n)
	}

	res, b = recordCall(t, srv, http.MethodPost, "/v1/projects/376187/sync", token, SyncRequest{Kinds: []SyncRequestKinds{}})
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
	res, b = recordCall(t, srv, http.MethodPost, "/v1/projects/376187/sync", token, SyncRequest{Kinds: []SyncRequestKinds{"punch"}})
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}

func TestGetRFIAttachment_StreamsAndRejectsForeign(t *testing.T) {
	srv, _, token := newRecordServer(t)

	path := "/v1/projects/376187/records/rfi/1001/attachments/" + strconv.FormatInt(attachmentID(1001), 10)
	res, b := recordCall(t, srv, http.MethodGet, path, token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("attachment: status %d; body %s", res.StatusCode, b)
	}
	if string(b) != "\xff\xd8jpeg-bytes" {
		t.Fatalf("body %q, want the file bytes", b)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type %q, want image/jpeg", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="photo.jpg"` {
		t.Fatalf("Content-Disposition %q", cd)
	}
	if ns := res.Header.Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Fatalf("X-Content-Type-Options %q, want nosniff", ns)
	}

	// An answer's attachment without a filename is named by its name.
	path = "/v1/projects/376187/records/rfi/1001/attachments/" + strconv.FormatInt(answerAttachmentID(1001), 10)
	res, b = recordCall(t, srv, http.MethodGet, path, token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer attachment: status %d; body %s", res.StatusCode, b)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="Sketch"` {
		t.Fatalf("answer Content-Disposition %q", cd)
	}

	// 1002's attachment is not on 1001.
	path = "/v1/projects/376187/records/rfi/1001/attachments/" + strconv.FormatInt(attachmentID(1002), 10)
	res, b = recordCall(t, srv, http.MethodGet, path, token, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")
}

func TestGetRFIAttachment_UpstreamFailureIs502(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	fp.With(func(f *fakeProcore) { delete(f.Files, "photo.jpg") })

	path := "/v1/projects/376187/records/rfi/1001/attachments/" + strconv.FormatInt(attachmentID(1001), 10)
	res, b := recordCall(t, srv, http.MethodGet, path, token, nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
}

func TestGetRFIAttachment_RFINotFoundIs404(t *testing.T) {
	srv, _, token := newRecordServer(t)

	res, b := recordCall(t, srv, http.MethodGet, "/v1/projects/376187/records/rfi/9999/attachments/1", token, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")
	if e := decode[Error](t, b); e.Message != "RFI not found" {
		t.Fatalf("message %q, want RFI not found", e.Message)
	}
}

func TestGetRFIAttachment_RefusesInsecureLocations(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	var plainHits atomic.Int32
	plainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write([]byte("plain"))
	}))
	t.Cleanup(plainSrv.Close)
	plain := plainSrv.URL + "/photo.jpg"
	fp.With(func(f *fakeProcore) {
		updated := f.Clock.now().Add(-time.Hour)
		f.RecordRFIs[testProcoreUser.ID] = append(f.RecordRFIs[testProcoreUser.ID],
			recordRFI(1003, "Plain http file", updated, plain),
			recordRFI(1004, "Redirected file", updated, f.Server.URL+"/files/moved.jpg"),
		)
		f.Files["moved.jpg"] = fakeFile{RedirectTo: plain}
	})

	path := "/v1/projects/376187/records/rfi/1003/attachments/" + strconv.FormatInt(attachmentID(1003), 10)
	res, b := recordCall(t, srv, http.MethodGet, path, token, nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
	if e := decode[Error](t, b); e.Message != "attachment location is not usable" {
		t.Fatalf("message %q", e.Message)
	}

	path = "/v1/projects/376187/records/rfi/1004/attachments/" + strconv.FormatInt(attachmentID(1004), 10)
	res, b = recordCall(t, srv, http.MethodGet, path, token, nil)
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
	if n := fp.requestsTo("/files/moved.jpg"); n != 1 {
		t.Fatalf("redirecting file requests %d, want 1", n)
	}
	if n := plainHits.Load(); n != 0 {
		t.Fatalf("plain http server hit %d times, want 0", n)
	}
}

func TestGetRecords_UndecodableBodyIsSkipped(t *testing.T) {
	srv, fp, token := newRecordServer(t)
	fp.With(func(f *fakeProcore) {
		bad := recordRFI(1003, "Broken", f.Clock.now().Add(-time.Hour), "")
		bad["assignees"] = "not a list"
		f.RecordRFIs[testProcoreUser.ID] = append(f.RecordRFIs[testProcoreUser.ID], bad)
	})

	page := getRecordPage(t, srv, token, 0)
	if len(page.Records) != 2 {
		t.Fatalf("records %d, want 2 with the undecodable one skipped", len(page.Records))
	}
	for _, r := range page.Records {
		if r.Id == 1003 {
			t.Fatal("undecodable record 1003 was returned")
		}
	}
}

func TestGetRecords_UnknownKindIs400(t *testing.T) {
	srv, _, token := newRecordServer(t)

	res, b := recordCall(t, srv, http.MethodGet, "/v1/projects/376187/records/punch?since=0", token, nil)
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
}
