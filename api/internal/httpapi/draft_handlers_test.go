package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

const (
	draftCompany = int64(7)
	draftProject = int64(9)
	draftFinal   = `{"payload":{"subject":"Twisted column","question_body":"Is the column OK?","assignee_ids":[11],"location_id":77},"unresolved":[],"confidence":{"subject":0.9}}`
)

// newDraftServer starts a test server where company 7 is visible and project
// 9 has Ben West (11) and the signed-in user (42) in its directory. The
// extractor answers with draftFinal.
func newDraftServer(t *testing.T) (testServer, string) {
	t.Helper()
	ts := newDraftTestServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.Companies = []procore.Company{{ID: draftCompany, Name: "Monroe Residential"}}
		f.ProjectDetails[draftProject] = procore.ProjectDetail{ID: draftProject, Name: "Elm Street", TimeZone: "Central Time (US & Canada)"}
		f.ProjectUsers[draftProject] = []procore.ProjectUser{
			{ID: 11, Name: "Ben West", JobTitle: "Architect"},
			{ID: 42, Name: "Pat Manager", JobTitle: "Superintendent"},
		}
	})
	ts.LLM.Steps = []llmtest.Step{{Final: draftFinal}}
	return ts, signIn(t, ts.Server, "device-1").AccessToken
}

// draftCall sends method path with X-Company-Id, a bearer token and an
// optional JSON body, and returns the response with its body read.
func draftCall(t *testing.T, srv *httptest.Server, method, path, bearer string, companyID int64, body any) (*http.Response, []byte) {
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
	req.Header.Set("X-Company-Id", strconv.FormatInt(companyID, 10))
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

func createDraftBody(id uuid.UUID) CreateDraftRequest {
	return CreateDraftRequest{
		Id: id, Feature: CreateDraftRequestFeatureRfi, ProjectId: draftProject,
		Transcript: "Column at C4 is twisted, send it to Ben West.", CapturedAt: time.Now(),
	}
}

// createDraft posts a new draft and fails the test unless it answers 200.
func createDraft(t *testing.T, srv *httptest.Server, token string, id uuid.UUID) DraftResponse {
	t.Helper()
	res, b := draftCall(t, srv, http.MethodPost, "/v1/drafts", token, draftCompany, createDraftBody(id))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create draft: status %d; body %s", res.StatusCode, b)
	}
	return decode[DraftResponse](t, b)
}

func TestCreateDraft_ReturnsDraftResponse(t *testing.T) {
	ts, token := newDraftServer(t)
	id := uuid.New()

	res, b := draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, createDraftBody(id))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	var raw struct {
		Status  string `json:"status"`
		Payload struct {
			AssigneeIDs     []int64          `json:"assignee_ids"`
			DistributionIDs *json.RawMessage `json:"distribution_ids"`
			RFIManagerID    int64            `json:"rfi_manager_id"`
		} `json:"payload"`
		Unresolved []struct {
			Field      string           `json:"field"`
			Candidates *json.RawMessage `json:"candidates"`
		} `json:"unresolved"`
		Confidence   map[string]float64 `json:"confidence"`
		ModelVersion string             `json:"model_version"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	if raw.Status != "extracted" {
		t.Errorf("status = %q, want extracted", raw.Status)
	}
	if len(raw.Payload.AssigneeIDs) != 1 || raw.Payload.AssigneeIDs[0] != 11 {
		t.Errorf("assignee_ids = %v, want [11]", raw.Payload.AssigneeIDs)
	}
	if raw.Payload.DistributionIDs == nil || string(*raw.Payload.DistributionIDs) != "[]" {
		t.Errorf("distribution_ids = %v, want []", raw.Payload.DistributionIDs)
	}
	if raw.Payload.RFIManagerID != 42 {
		t.Errorf("rfi_manager_id = %d, want 42", raw.Payload.RFIManagerID)
	}
	if len(raw.Unresolved) != 1 || raw.Unresolved[0].Field != "location_id" ||
		raw.Unresolved[0].Candidates == nil || string(*raw.Unresolved[0].Candidates) != "[]" {
		t.Errorf("unresolved = %s", b)
	}
	if raw.Confidence["subject"] != 0.9 || raw.ModelVersion != "claude-test-model" {
		t.Errorf("confidence %v model %q", raw.Confidence, raw.ModelVersion)
	}

	got := decode[DraftResponse](t, b)
	if got.Id != id || got.ProjectId != draftProject || got.Feature != DraftResponseFeatureRfi {
		t.Errorf("draft = %+v", got)
	}

	// Repeating the call returns the stored draft without extracting again.
	again := createDraft(t, ts.Server, token, id)
	if again.Id != id || len(ts.LLM.Requests) != 1 {
		t.Errorf("repeat: id %s, extractions %d", again.Id, len(ts.LLM.Requests))
	}
}

func TestCreateDraft_CompanyNotVisible_403(t *testing.T) {
	ts, token := newDraftServer(t)
	res, b := draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, 8, createDraftBody(uuid.New()))
	wantError(t, res, b, http.StatusForbidden, "forbidden_scope")
	if len(ts.LLM.Requests) != 0 {
		t.Errorf("extractions = %d, want 0", len(ts.LLM.Requests))
	}
}

func TestCreateDraft_ExtractFails_502(t *testing.T) {
	ts, token := newDraftServer(t)
	ts.LLM.Steps = []llmtest.Step{{Err: errors.New("timeout")}}
	id := uuid.New()

	res, b := draftCall(t, ts.Server, http.MethodPost, "/v1/drafts", token, draftCompany, createDraftBody(id))
	wantError(t, res, b, http.StatusBadGateway, "procore_unavailable")
	if e := decode[Error](t, b); e.Message != "extraction failed" {
		t.Errorf("message = %q", e.Message)
	}
	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")
}

func TestGetDraft_OtherUser_404(t *testing.T) {
	ts, token := newDraftServer(t)
	ts.Procore.With(func(f *fakeProcore) {
		f.Companies = append(f.Companies, procore.Company{ID: 8, Name: "Other Co"})
	})
	id := uuid.New()
	createDraft(t, ts.Server, token, id)

	res, b := draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if res.StatusCode != http.StatusOK || decode[DraftResponse](t, b).Id != id {
		t.Fatalf("owner GET: status %d; body %s", res.StatusCode, b)
	}

	ts.Procore.With(func(f *fakeProcore) {
		f.Users[11] = procore.Me{ID: 11, Login: "ben@example.com", Name: "Ben West"}
	})
	other := signInAs(t, ts.Server, "device-2", "11").AccessToken
	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), other, draftCompany, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")

	// The owner cannot reach the draft through another visible company.
	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, 8, nil)
	wantError(t, res, b, http.StatusNotFound, "not_found")
}

func TestSaveDraft_StoresPayload(t *testing.T) {
	ts, token := newDraftServer(t)
	id := uuid.New()
	created := createDraft(t, ts.Server, token, id)

	edited := created.Payload
	subject := "Twisted column at C4"
	edited.Subject = &subject
	edited.AssigneeIds = []int64{11, 99}
	res, b := draftCall(t, ts.Server, http.MethodPatch, "/v1/drafts/"+id.String(), token, draftCompany, SaveDraftRequest{Payload: edited})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, b)
	}
	saved := decode[DraftResponse](t, b)
	if saved.Status != DraftResponseStatusReviewed || saved.Payload.Subject == nil || *saved.Payload.Subject != subject {
		t.Errorf("saved = %+v", saved)
	}
	if len(saved.Payload.AssigneeIds) != 1 || saved.Payload.AssigneeIds[0] != 11 {
		t.Errorf("assignee_ids = %v, want [11]", saved.Payload.AssigneeIds)
	}
	if len(saved.Unresolved) != 1 || saved.Unresolved[0].Field != "assignee_ids" || saved.Unresolved[0].Reason != NotInLookup {
		t.Errorf("unresolved = %+v", saved.Unresolved)
	}

	res, b = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts/"+id.String(), token, draftCompany, nil)
	if got := decode[DraftResponse](t, b); res.StatusCode != http.StatusOK || got.Payload.Subject == nil || *got.Payload.Subject != subject {
		t.Errorf("GET after save: status %d; body %s", res.StatusCode, b)
	}
}

func TestListDrafts_NewestFirst(t *testing.T) {
	ts, token := newDraftServer(t)
	a, b := uuid.New(), uuid.New()
	createDraft(t, ts.Server, token, a)
	createDraft(t, ts.Server, token, b)

	path := "/v1/drafts?project_id=" + strconv.FormatInt(draftProject, 10)
	res, body := draftCall(t, ts.Server, http.MethodGet, path, token, draftCompany, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d; body %s", res.StatusCode, body)
	}
	list := decode[[]DraftResponse](t, body)
	if len(list) != 2 || list[0].Id != b || list[1].Id != a {
		t.Fatalf("list = %s", body)
	}

	res, body = draftCall(t, ts.Server, http.MethodGet, path+"&limit=1", token, draftCompany, nil)
	if list := decode[[]DraftResponse](t, body); res.StatusCode != http.StatusOK || len(list) != 1 || list[0].Id != b {
		t.Fatalf("limit 1: status %d; body %s", res.StatusCode, body)
	}

	res, body = draftCall(t, ts.Server, http.MethodGet, "/v1/drafts?project_id=5", token, draftCompany, nil)
	if res.StatusCode != http.StatusOK || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("other project: status %d; body %s", res.StatusCode, body)
	}
}
