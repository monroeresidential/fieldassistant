package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// newSubmitServer is newDraftServer with location 77 in the project, so a
// new draft has nothing unresolved, and Procore answering an RFI create
// with id 501, number 214.
func newSubmitServer(t *testing.T) (testServer, string) {
	t.Helper()
	ts, token := newDraftServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.Locations[draftProject] = []procore.Location{{ID: 77, Name: "Grid C4"}}
		f.RFICreateResponse = func([]byte) (int, string) {
			return http.StatusCreated, `{"id":501,"number":214,"status":"draft","link":"https://app.procore.com/rfis/501"}`
		}
	})
	return ts, token
}

// submitCall posts /drafts/{id}/submit with key as the Idempotency-Key.
func submitCall(t *testing.T, srv *httptest.Server, token string, id uuid.UUID, key string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/drafts/"+id.String()+"/submit", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Company-Id", strconv.FormatInt(draftCompany, 10))
	req.Header.Set("Idempotency-Key", key)
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, b
}

// waitJob polls GET /jobs/{id} until the job leaves queued and running, or
// fails the test after 10 s.
func waitJob(t *testing.T, srv *httptest.Server, token, jobID string) JobStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, b := draftCall(t, srv, http.MethodGet, "/v1/jobs/"+jobID, token, draftCompany, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("get job: status %d; body %s", res.StatusCode, b)
		}
		js := decode[JobStatus](t, b)
		if js.Status != JobStatusStatusQueued && js.Status != JobStatusStatusRunning {
			return js
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s after 10s", jobID, js.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// submitted creates a draft, submits it and waits for the job to finish.
func submitted(t *testing.T, ts testServer, token string) (uuid.UUID, JobStatus) {
	t.Helper()
	id := uuid.New()
	createDraft(t, ts.Server, token, id)
	res, b := submitCall(t, ts.Server, token, id, id.String())
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("submit: status %d; body %s", res.StatusCode, b)
	}
	sr := decode[SubmitResponse](t, b)
	if sr.Status != SubmitResponseStatusSubmitting || sr.DraftId != id || sr.JobId == "" {
		t.Fatalf("submit response = %+v", sr)
	}
	return id, waitJob(t, ts.Server, token, sr.JobId)
}

func TestSubmitDraft_Enqueues202ThenJobSucceeds(t *testing.T) {
	ts, token := newSubmitServer(t)
	id, js := submitted(t, ts, token)

	if js.Status != JobStatusStatusSucceeded || js.DraftId != id || js.Error != nil {
		t.Fatalf("job = %+v", js)
	}
	if js.Result == nil || js.Result.ProcoreId != 501 || js.Result.Number == nil || *js.Result.Number != "214" {
		t.Fatalf("job result = %+v", js.Result)
	}

	res, b := draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get draft: status %d; body %s", res.StatusCode, b)
	}
	d := decode[DraftResponse](t, b)
	if d.Status != DraftResponseStatusSubmitted || d.Result == nil || d.Result.Number == nil || *d.Result.Number != "214" {
		t.Fatalf("draft = status %q result %+v", d.Status, d.Result)
	}
	var posts int
	ts.Procore.With(func(f *fakeProcore) { posts = len(f.RFICreateBodies) })
	if posts != 1 {
		t.Errorf("Procore received %d RFI posts, want 1", posts)
	}
}

func TestSubmitDraft_KeyMismatch_400(t *testing.T) {
	ts, token := newSubmitServer(t)
	id := uuid.New()
	createDraft(t, ts.Server, token, id)

	res, b := submitCall(t, ts.Server, token, id, uuid.NewString())
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
	if e := decode[Error](t, b); e.Message != "Idempotency-Key must equal the draft id" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestSubmitDraft_Unresolved_400(t *testing.T) {
	// Without location 77 in the project, the extraction leaves location_id
	// unresolved.
	ts, token := newDraftServer(t)
	id := uuid.New()
	createDraft(t, ts.Server, token, id)

	res, b := submitCall(t, ts.Server, token, id, id.String())
	wantError(t, res, b, http.StatusBadRequest, "bad_request")
	if e := decode[Error](t, b); !strings.Contains(e.Message, "location_id") {
		t.Errorf("message = %q, want it to name location_id", e.Message)
	}
	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if d := decode[DraftResponse](t, b); res.StatusCode != http.StatusOK || d.Status != DraftResponseStatusExtracted {
		t.Errorf("draft after rejected submit: status %d, %q", res.StatusCode, d.Status)
	}
}

func TestSubmitDraft_AlreadySubmitted_200(t *testing.T) {
	ts, token := newSubmitServer(t)
	id, js := submitted(t, ts, token)
	if js.Status != JobStatusStatusSucceeded {
		t.Fatalf("job = %+v", js)
	}

	res, b := submitCall(t, ts.Server, token, id, id.String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resubmit: status %d; body %s", res.StatusCode, b)
	}
	if sr := decode[SubmitResponse](t, b); sr.Status != SubmitResponseStatusSubmitted || sr.DraftId != id {
		t.Errorf("resubmit response = %+v", sr)
	}
	var posts int
	ts.Procore.With(func(f *fakeProcore) { posts = len(f.RFICreateBodies) })
	if posts != 1 {
		t.Errorf("Procore received %d RFI posts, want 1", posts)
	}
}

func TestSubmitDraft_OtherUser_404(t *testing.T) {
	ts, token := newSubmitServer(t)
	id := uuid.New()
	createDraft(t, ts.Server, token, id)
	ts.Procore.With(func(f *fakeProcore) {
		f.Users[11] = procore.Me{ID: 11, Login: "ben@example.com", Name: "Ben West"}
	})
	other := signInAs(t, ts.Server, "device-2", "11").AccessToken

	res, b := submitCall(t, ts.Server, other, id, id.String())
	wantError(t, res, b, http.StatusNotFound, "not_found")
}

func TestGetJob_422_FailedWithDraftError(t *testing.T) {
	ts, token := newSubmitServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.RFICreateResponse = func([]byte) (int, string) {
			return http.StatusUnprocessableEntity, `{"errors":{"question.body":["is too short"]}}`
		}
	})
	id, js := submitted(t, ts, token)

	if js.Status != JobStatusStatusFailed || js.Error == nil || js.Error.Code != "procore_validation" || js.Result != nil {
		t.Fatalf("job = %+v", js)
	}
	res, b := draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get draft: status %d; body %s", res.StatusCode, b)
	}
	d := decode[DraftResponse](t, b)
	if d.Status != DraftResponseStatusFailed || len(d.Unresolved) != 1 || d.Unresolved[0].Field != "question_body" ||
		d.Unresolved[0].Reason != ProcoreValidation {
		t.Errorf("draft = status %q unresolved %+v", d.Status, d.Unresolved)
	}
}

// After a rejected submit and a successful resubmit, the first job reports
// River's state alone, not the draft's later outcome.
func TestGetJob_EarlierJob_NoDraftOverlay(t *testing.T) {
	ts, token := newSubmitServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.RFICreateResponse = func([]byte) (int, string) {
			return http.StatusUnprocessableEntity, `{"errors":{"base":["Workflow not configured"]}}`
		}
	})
	id, first := submitted(t, ts, token)
	if first.Status != JobStatusStatusFailed {
		t.Fatalf("first job = %+v", first)
	}

	ts.Procore.With(func(f *fakeProcore) {
		f.RFICreateResponse = func([]byte) (int, string) {
			return http.StatusCreated, `{"id":501,"number":214}`
		}
	})
	res, b := submitCall(t, ts.Server, token, id, id.String())
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("resubmit: status %d; body %s", res.StatusCode, b)
	}
	sr := decode[SubmitResponse](t, b)
	if sr.JobId == first.Id {
		t.Fatalf("resubmit reused job %s", sr.JobId)
	}
	if second := waitJob(t, ts.Server, token, sr.JobId); second.Status != JobStatusStatusSucceeded || second.Result == nil {
		t.Fatalf("second job = %+v", second)
	}

	again := waitJob(t, ts.Server, token, first.Id)
	if again.Status != JobStatusStatusSucceeded || again.Result != nil || again.Error != nil {
		t.Errorf("first job now = %+v, want River's completed state with no draft outcome", again)
	}
}

func TestGetJob_OtherUser_404(t *testing.T) {
	ts, token := newSubmitServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.Companies = append(f.Companies, procore.Company{ID: 8, Name: "Other Co"})
	})
	_, js := submitted(t, ts, token)

	ts.Procore.With(func(f *fakeProcore) {
		f.Users[11] = procore.Me{ID: 11, Login: "ben@example.com", Name: "Ben West"}
	})
	other := signInAs(t, ts.Server, "device-2", "11").AccessToken
	res, b := draftCall(t, ts.Server, http.MethodGet, "/v1/jobs/"+js.Id, other, draftCompany, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")

	// The owner cannot reach the job through another visible company.
	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/jobs/"+js.Id, token, 8, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")

	for _, bad := range []string{"999999", "not-a-number"} {
		res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/jobs/"+bad, token, draftCompany, nil)
		wantError(t, res, b, http.StatusNotFound, "not_found")
	}
}
