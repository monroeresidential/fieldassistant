package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline/pipelinetest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// draftsFeature is a minimal RFI-shaped feature, so the pipeline tests do not
// depend on the rfi package (which imports pipeline).
type draftsFeature struct{}

func (draftsFeature) Name() string { return "rfi" }
func (draftsFeature) LookupKinds() []LookupKind {
	return []LookupKind{KindUsers, KindLocations, KindDrawings}
}
func (draftsFeature) IDFields() []IDField {
	return []IDField{
		{Field: "assignee_ids", Kind: KindUsers, Multi: true},
		{Field: "rfi_manager_id", Kind: KindUsers},
		{Field: "location_id", Kind: KindLocations},
	}
}
func (draftsFeature) DateFields() []string { return []string{"due_date"} }
func (draftsFeature) PayloadFields() []string {
	return []string{"subject", "question_body", "assignee_ids", "rfi_manager_id", "location_id", "due_date", "draft"}
}
func (draftsFeature) Prompt() llm.Prompt {
	return llm.Prompt{Text: "system prompt", Version: "abcdef012345"}
}
func (draftsFeature) OutputSchema() map[string]any { return map[string]any{"type": "object"} }
func (draftsFeature) UserMessage(ctx ExtractContext, transcript string) string {
	return ctx.ProjectName + "|" + ctx.TimeZone + "|" + ctx.UserName + "|" + transcript
}
func (draftsFeature) ApplyDefaults(ctx ExtractContext, payload map[string]any, _ []Unresolved, _ map[LookupKind]LookupSet) (map[string]any, []Unresolved) {
	out := map[string]any{}
	for k, v := range payload {
		out[k] = v
	}
	if out["rfi_manager_id"] == nil {
		out["rfi_manager_id"] = ctx.UserProcoreID
	}
	if out["draft"] == nil {
		out["draft"] = ctx.SubmittedCount < 5
	}
	return out, nil
}

type draftsEnv struct {
	drafts *Drafts
	llm    *llmtest.Scripted
	fp     *pipelinetest.FakeProcore
	st     *store.Store
	clock  *testClock
	user   uuid.UUID
}

func newDraftsEnv(t *testing.T) *draftsEnv {
	t.Helper()
	l, fp, clock, st := newLookups(t)
	fp.Users = []procore.ProjectUser{
		{ID: 11, Name: "Ben West", JobTitle: "Architect"},
		{ID: 42, Name: "Pat Field", JobTitle: "Superintendent"},
	}
	fp.Project = procore.ProjectDetail{ID: testProject, Name: "Tower", DisplayName: "Tower One", TimeZone: "Central Time (US & Canada)"}
	scripted := &llmtest.Scripted{Model: "claude-test-model"}
	d := NewDrafts(st, l, scripted, map[string]Feature{"rfi": draftsFeature{}}, "claude-configured", clock.Now, nil)
	return &draftsEnv{drafts: d, llm: scripted, fp: fp, st: st, clock: clock, user: newDBUser(t, st, 42, "Pat Field")}
}

func newDBUser(t *testing.T, st *store.Store, procoreID int64, name string) uuid.UUID {
	t.Helper()
	u, err := st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{
		ProcoreUserID: procoreID, Email: name + "@example.com", Name: name,
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return uuid.UUID(u.ID.Bytes)
}

func (e *draftsEnv) input(id uuid.UUID) CreateInput {
	return CreateInput{
		ID: id, UserID: e.user, CompanyID: testCompany, ProjectID: testProject,
		Feature: "rfi", Transcript: "The column at grid C4 is twisted, ask Ben West.",
		CapturedAt: e.clock.Now(),
	}
}

const twistedFinal = `{"payload":{"subject":"Twisted column","question_body":"Is it OK?","assignee_ids":[11],"location_id":77},"unresolved":[],"confidence":{"subject":0.9}}`

func payloadMap(t *testing.T, d Draft) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(d.Payload(), &m); err != nil {
		t.Fatalf("decode payload %s: %v", d.Payload(), err)
	}
	return m
}

func auditEvents(t *testing.T, st *store.Store, id uuid.UUID) []db.AuditEvent {
	t.Helper()
	evs, err := st.Q.ListAuditEvents(context.Background(), pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	return evs
}

func TestCreate_ExtractsValidatesAndAudits(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Tool: "list_users", Input: `{"query":"west"}`}, {Final: twistedFinal}}
	id := uuid.New()

	d, err := e.drafts.Create(context.Background(), e.input(id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.Status != StatusExtracted || d.ID != id || d.UserID != e.user || d.CompanyID != testCompany {
		t.Fatalf("draft = %+v", d)
	}
	p := payloadMap(t, d)
	if ids, _ := p["assignee_ids"].([]any); len(ids) != 1 || ids[0] != float64(11) {
		t.Errorf("assignee_ids = %v, want [11]", p["assignee_ids"])
	}
	if _, ok := p["location_id"]; ok {
		t.Errorf("location_id = %v, want absent", p["location_id"])
	}
	if p["rfi_manager_id"] != float64(42) || p["draft"] != true {
		t.Errorf("defaults: rfi_manager_id %v draft %v", p["rfi_manager_id"], p["draft"])
	}
	if len(d.Unresolved) != 1 || d.Unresolved[0].Field != "location_id" || d.Unresolved[0].Reason != ReasonNotInLookup {
		t.Errorf("unresolved = %+v", d.Unresolved)
	}
	if d.Confidence["subject"] != 0.9 {
		t.Errorf("confidence = %v", d.Confidence)
	}
	if d.ModelVersion != "claude-test-model" || len(d.PromptVersion) != 12 {
		t.Errorf("model %q prompt %q", d.ModelVersion, d.PromptVersion)
	}

	req := e.llm.Requests[0]
	if req.FinalTool != "submit_draft" || req.MaxToolCalls != 6 || req.System != "system prompt" {
		t.Errorf("request = %+v", req)
	}
	if want := "Tower One|America/Chicago|Pat Field|" + e.input(id).Transcript; req.User != want {
		t.Errorf("user message = %q, want %q", req.User, want)
	}

	got, err := e.drafts.Get(context.Background(), e.user, id)
	if err != nil || got.Status != StatusExtracted || len(got.Unresolved) != 1 {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	evs := auditEvents(t, e.st, id)
	if len(evs) != 1 || evs[0].Kind != "extracted" || evs[0].CompanyID != testCompany {
		t.Fatalf("audit = %+v", evs)
	}
	var ap struct {
		Turns     []json.RawMessage `json:"turns"`
		ToolCalls int               `json:"tool_calls"`
		RawFinal  json.RawMessage   `json:"raw_final"`
	}
	if err := json.Unmarshal(evs[0].Payload, &ap); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	if len(ap.Turns) == 0 || ap.ToolCalls != 1 || len(ap.RawFinal) == 0 {
		t.Errorf("audit payload = %s", evs[0].Payload)
	}
}

func TestCreate_SameID_ReturnsExistingWithoutExtracting(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	id := uuid.New()
	ctx := context.Background()

	first, err := e.drafts.Create(ctx, e.input(id))
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := e.drafts.Create(ctx, e.input(id))
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if len(e.llm.Requests) != 1 {
		t.Errorf("extractor calls = %d, want 1", len(e.llm.Requests))
	}
	if second.ID != first.ID || string(second.Payload()) != string(first.Payload()) {
		t.Errorf("second = %+v, want %+v", second, first)
	}
}

func TestCreate_SameID_OtherUser_NotFound(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	id := uuid.New()
	ctx := context.Background()
	if _, err := e.drafts.Create(ctx, e.input(id)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	in := e.input(id)
	in.UserID = newDBUser(t, e.st, 11, "Ben West")
	if _, err := e.drafts.Create(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's Create err = %v, want ErrNotFound", err)
	}
	if _, err := e.drafts.Get(ctx, in.UserID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's Get err = %v, want ErrNotFound", err)
	}
}

func TestCreate_UnavailableKind_ToolOmitted(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Err["ListDrawings"] = fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403, Body: "no drawings"})
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}

	if _, err := e.drafts.Create(context.Background(), e.input(uuid.New())); err != nil {
		t.Fatalf("Create: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range e.llm.Requests[0].Tools {
		names[tool.Name] = true
	}
	if names["list_drawings"] || !names["list_users"] || !names["list_locations"] || !names["submit_draft"] {
		t.Errorf("tools = %v", names)
	}
}

func TestCreate_ToolHandler_FiltersRows(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{
		{Tool: "list_users", Input: `{"query":"ben"}`},
		{Tool: "list_users", Input: `{"query":"SUPERINTENDENT"}`},
		{Tool: "list_users", Input: `{}`},
		{Final: twistedFinal},
	}
	if _, err := e.drafts.Create(context.Background(), e.input(uuid.New())); err != nil {
		t.Fatalf("Create: %v", err)
	}
	type result struct {
		Rows  []LookupRow `json:"rows"`
		Total int         `json:"total"`
	}
	var r [3]result
	for i := range r {
		if err := json.Unmarshal(e.llm.Results[i], &r[i]); err != nil {
			t.Fatalf("decode result %d %s: %v", i, e.llm.Results[i], err)
		}
	}
	if len(r[0].Rows) != 1 || r[0].Rows[0].ID != 11 || r[0].Rows[0].Label != "Ben West" || r[0].Total != 1 {
		t.Errorf("query ben = %+v", r[0])
	}
	if len(r[1].Rows) != 1 || r[1].Rows[0].ID != 42 {
		t.Errorf("query by extra role = %+v", r[1])
	}
	if len(r[2].Rows) != 2 || r[2].Total != 2 {
		t.Errorf("empty query = %+v", r[2])
	}
}

func TestToolHandler_CapsRowsAt25(t *testing.T) {
	rows := make([]LookupRow, 30)
	for i := range rows {
		rows[i] = LookupRow{ID: int64(i + 1), Label: "Person"}
	}
	h := toolHandler(map[LookupKind]LookupSet{KindUsers: {Kind: KindUsers, Rows: rows}})
	out, err := h(context.Background(), llm.ToolCall{Name: "list_users", Input: json.RawMessage(`{"query":"person"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var r struct {
		Rows  []LookupRow `json:"rows"`
		Total int         `json:"total"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 25 || r.Total != 30 {
		t.Errorf("rows %d total %d, want 25 and 30", len(r.Rows), r.Total)
	}
	if _, err := h(context.Background(), llm.ToolCall{Name: "list_vendors", Input: json.RawMessage(`{}`)}); err == nil {
		t.Error("unknown tool: want error")
	}
}

func TestCreate_ExtractorError_NoDraft(t *testing.T) {
	e := newDraftsEnv(t)
	boom := errors.New("timeout")
	e.llm.Steps = []llmtest.Step{{Err: boom}}
	id := uuid.New()

	_, err := e.drafts.Create(context.Background(), e.input(id))
	if !errors.Is(err, ErrExtractFailed) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want ErrExtractFailed wrapping timeout", err)
	}
	if _, err := e.st.Q.GetDraft(context.Background(), pgtype.UUID{Bytes: id, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetDraft err = %v, want no rows", err)
	}
}

func TestSaveEdits_ValidatesAndAudits(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	id := uuid.New()
	ctx := context.Background()
	created, err := e.drafts.Create(ctx, e.input(id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	edit := json.RawMessage(`{"subject":"Twisted column C4","assignee_ids":[99],"rfi_manager_id":42,"draft":true}`)
	d, err := e.drafts.SaveEdits(ctx, e.user, id, edit)
	if err != nil {
		t.Fatalf("SaveEdits: %v", err)
	}
	if d.Status != StatusReviewed {
		t.Errorf("status = %q, want reviewed", d.Status)
	}
	p := payloadMap(t, d)
	if ids, ok := p["assignee_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("assignee_ids = %v, want []", p["assignee_ids"])
	}
	if p["subject"] != "Twisted column C4" {
		t.Errorf("subject = %v", p["subject"])
	}
	// The new bad assignee is flagged first; the extraction's location_id
	// flag stays because the edit left location_id empty.
	if len(d.Unresolved) != 2 || d.Unresolved[0].Field != "assignee_ids" || d.Unresolved[0].Reason != ReasonNotInLookup ||
		d.Unresolved[1].Field != "location_id" {
		t.Errorf("unresolved = %+v", d.Unresolved)
	}
	if string(d.LLMOutput) != string(created.LLMOutput) {
		t.Errorf("llm output changed: %s", d.LLMOutput)
	}

	evs := auditEvents(t, e.st, id)
	if len(evs) != 2 || evs[1].Kind != "edited" {
		t.Fatalf("audit = %+v", evs)
	}
	var ap struct {
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	if err := json.Unmarshal(evs[1].Payload, &ap); err != nil {
		t.Fatal(err)
	}
	if ap.Before["subject"] != "Twisted column" || ap.After["subject"] != "Twisted column C4" {
		t.Errorf("edited payload = %s", evs[1].Payload)
	}

	if _, err := e.drafts.SaveEdits(ctx, newDBUser(t, e.st, 11, "Ben West"), id, edit); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user's SaveEdits err = %v, want ErrNotFound", err)
	}
}

func TestSaveEdits_KeepsFlagsForStillEmptyFields(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: `{"payload":{"subject":"Twisted column","assignee_ids":[11]},` +
		`"unresolved":[{"field":"rfi_manager_id","reason":"not_found","candidates":[]},{"field":"_form","reason":"not_found","candidates":[]}],"confidence":{}}`}}
	id := uuid.New()
	ctx := context.Background()
	created, err := e.drafts.Create(ctx, e.input(id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Unresolved) != 1 || created.Unresolved[0].Field != "rfi_manager_id" {
		t.Fatalf("created unresolved = %+v", created.Unresolved)
	}
	pid := pgtype.UUID{Bytes: id, Valid: true}
	if err := e.st.Q.SetDraftFailed(ctx, db.SetDraftFailedParams{
		ID: pid, ErrorCode: pgtype.Text{String: "procore_validation", Valid: true},
		ErrorMessage: pgtype.Text{String: "rfi_manager_id can't be blank", Valid: true},
		Unresolved:   []byte(`[{"field":"rfi_manager_id","reason":"procore_validation","message":"can't be blank","candidates":[]}]`),
	}); err != nil {
		t.Fatal(err)
	}

	// rfi_manager_id still empty: the flag stays, and the failure is cleared.
	d, err := e.drafts.SaveEdits(ctx, e.user, id, json.RawMessage(`{"subject":"Twisted column","assignee_ids":[11],"rfi_manager_id":null}`))
	if err != nil {
		t.Fatalf("first SaveEdits: %v", err)
	}
	if len(d.Unresolved) != 1 || d.Unresolved[0].Field != "rfi_manager_id" || d.Unresolved[0].Reason != ReasonProcoreValidation {
		t.Errorf("unresolved after empty save = %+v", d.Unresolved)
	}
	if d.Status != StatusReviewed || d.Error != nil {
		t.Errorf("status %q error %+v, want reviewed and no error", d.Status, d.Error)
	}

	// rfi_manager_id set to a user in the directory: the flag is gone.
	d, err = e.drafts.SaveEdits(ctx, e.user, id, json.RawMessage(`{"subject":"Twisted column","assignee_ids":[11],"rfi_manager_id":42}`))
	if err != nil {
		t.Fatalf("second SaveEdits: %v", err)
	}
	if len(d.Unresolved) != 0 {
		t.Errorf("unresolved after filling = %+v, want none", d.Unresolved)
	}
	if p := payloadMap(t, d); p["rfi_manager_id"] != float64(42) {
		t.Errorf("rfi_manager_id = %v, want 42", p["rfi_manager_id"])
	}
}

func TestSaveEdits_Submitted_Locked(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	id := uuid.New()
	ctx := context.Background()
	if _, err := e.drafts.Create(ctx, e.input(id)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := e.st.Q.SetDraftStatus(ctx, db.SetDraftStatusParams{ID: pgtype.UUID{Bytes: id, Valid: true}, Status: StatusSubmitting}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.drafts.SaveEdits(ctx, e.user, id, json.RawMessage(`{"subject":"x"}`)); !errors.Is(err, ErrDraftLocked) {
		t.Fatalf("err = %v, want ErrDraftLocked", err)
	}
}

func TestList_NewestFirstForUserProject(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Final: twistedFinal}}
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	if _, err := e.drafts.Create(ctx, e.input(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.drafts.Create(ctx, e.input(b)); err != nil {
		t.Fatal(err)
	}
	list, err := e.drafts.List(ctx, e.user, testProject, 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != b || list[1].ID != a {
		t.Errorf("list order = %v", list)
	}
	if list, _ := e.drafts.List(ctx, e.user, testProject, 1); len(list) != 1 {
		t.Errorf("limit 1 = %d drafts", len(list))
	}
}

func TestRailsZone_Central(t *testing.T) {
	if got := RailsZone("Central Time (US & Canada)"); got.String() != "America/Chicago" {
		t.Errorf("Central = %s", got)
	}
	if got := RailsZone("America/Denver"); got.String() != "America/Denver" {
		t.Errorf("IANA = %s", got)
	}
	if got := RailsZone("Mars Standard Time"); got != time.UTC {
		t.Errorf("unknown = %s, want UTC", got)
	}
}

func TestCreate_ConcurrentSameID_OneExtraction(t *testing.T) {
	e := newDraftsEnv(t)
	e.llm.Steps = []llmtest.Step{{Tool: "list_users", Input: `{"query":"west"}`}, {Final: twistedFinal}}
	id := uuid.New()
	ctx := context.Background()
	// Warm the cache first so the racing calls reach the extractor together.
	if _, err := e.drafts.lookups.Warm(ctx, e.user, testCompany, testProject, draftsFeature{}.LookupKinds()); err != nil {
		t.Fatal(err)
	}

	const n = 8
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := e.drafts.Create(ctx, e.input(id))
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if len(e.llm.Requests) != 1 {
		t.Errorf("extractor calls = %d, want 1", len(e.llm.Requests))
	}
}
