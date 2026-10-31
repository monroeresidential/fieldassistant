package rfi

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

var _ pipeline.Feature = (*Feature)(nil)

// An impact value must be a plain number. Currency formatting is cleaned
// up; words are kept for the reviewer and the field is flagged, so the
// problem shows on the review screen instead of after Submit.
func TestValidate_ImpactValue_NonNumeric_Flagged(t *testing.T) {
	f := New()
	payload := map[string]any{
		"subject":         "Twisted column",
		"schedule_impact": map[string]any{"status": "yes_known", "value": "two weeks"},
		"cost_impact":     map[string]any{"status": "yes_known", "value": " $2,000 "},
	}
	norm, un := f.Normalize(payload, nil)
	out, un := pipeline.Validate(f, norm, un, usersWith(), time.Now(), time.UTC)

	if len(un) != 1 || un[0].Field != "schedule_impact" || un[0].Reason != pipeline.ReasonInvalidValue || un[0].Message != pipeline.ImpactValueMessage {
		t.Errorf("unresolved = %+v, want schedule_impact invalid_value", un)
	}
	if v := out["schedule_impact"].(map[string]any)["value"]; v != "two weeks" {
		t.Errorf("schedule_impact.value = %v, want kept as spoken", v)
	}
	if v := out["cost_impact"].(map[string]any)["value"]; v != "2000" {
		t.Errorf("cost_impact.value = %v, want 2000", v)
	}
	if v := payload["cost_impact"].(map[string]any)["value"]; v != " $2,000 " {
		t.Errorf("input mutated: %v", v)
	}

	// A number is written as its decimal string.
	norm, un = f.Normalize(map[string]any{"cost_impact": map[string]any{"status": "yes_known", "value": 1500.5}}, nil)
	if v := norm["cost_impact"].(map[string]any)["value"]; v != "1500.5" || len(un) != 0 {
		t.Errorf("numeric value = %v, unresolved %+v", v, un)
	}
}

func TestNormalize_DropsWrongTypesAndMapsReasons(t *testing.T) {
	f := New()
	payload := map[string]any{
		"subject":          float64(5),
		"question_body":    "Q",
		"location_id":      "77",
		"private":          "no",
		"due_date":         nil,
		"cost_impact":      map[string]any{"status": "maybe"},
		"schedule_impact":  map[string]any{"status": "no", "extra": true},
		"made_up_field":    "x",
		"distribution_ids": []any{float64(3), "4", nil},
	}
	un := []pipeline.Unresolved{
		{Field: "assignee_ids", Reason: "weird"},
		{Field: "location_id", Reason: pipeline.ReasonNotFound},
	}
	out, got := f.Normalize(payload, un)

	want := map[string]any{
		"question_body":    "Q",
		"location_id":      float64(77),
		"due_date":         nil,
		"schedule_impact":  map[string]any{"status": "no"},
		"distribution_ids": []any{float64(3), float64(4)},
	}
	if b1, b2 := mustJSON(t, out), mustJSON(t, want); b1 != b2 {
		t.Errorf("payload = %s, want %s", b1, b2)
	}
	if len(got) != 3 || got[0].Reason != pipeline.ReasonAmbiguous || got[1].Reason != pipeline.ReasonNotFound ||
		got[2].Field != "cost_impact" || got[2].Reason != pipeline.ReasonInvalidValue || got[2].Message != "invalid cost impact status" {
		t.Errorf("unresolved = %+v", got)
	}
	if un[0].Reason != "weird" {
		t.Error("input unresolved mutated")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func usersWith(ids ...int64) map[pipeline.LookupKind]pipeline.LookupSet {
	rows := []pipeline.LookupRow{}
	for _, id := range ids {
		rows = append(rows, pipeline.LookupRow{ID: id, Label: "U"})
	}
	return map[pipeline.LookupKind]pipeline.LookupSet{pipeline.KindUsers: {Kind: pipeline.KindUsers, Rows: rows}}
}

// managersWith is usersWith plus Procore's potential RFI managers.
func managersWith(users []int64, managers ...int64) map[pipeline.LookupKind]pipeline.LookupSet {
	sets := usersWith(users...)
	rows := []pipeline.LookupRow{}
	for _, id := range managers {
		rows = append(rows, pipeline.LookupRow{ID: id, Label: "M"})
	}
	sets[pipeline.KindRFIManagers] = pipeline.LookupSet{Kind: pipeline.KindRFIManagers, Rows: rows}
	return sets
}

func ctxFor(userID int64, submitted int) pipeline.ExtractContext {
	return pipeline.ExtractContext{
		ProjectName: "Tower", TimeZone: "America/Chicago",
		Today:    time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		UserName: "Sam Super", UserProcoreID: userID, SubmittedCount: submitted,
		Transcript: "um why",
	}
}

func TestRFI_OutputSchema_IsValidJSONSchema(t *testing.T) {
	b, err := json.Marshal(New().OutputSchema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var s struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" {
		t.Errorf("type = %q", s.Type)
	}
	want := map[string]bool{"payload": true, "unresolved": true, "confidence": true}
	if len(s.Required) != 3 {
		t.Fatalf("required = %v", s.Required)
	}
	for _, r := range s.Required {
		if !want[r] || s.Properties[r] == nil {
			t.Errorf("required %q missing or without a property", r)
		}
	}
	var payload struct {
		AdditionalProperties *bool          `json:"additionalProperties"`
		Properties           map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(s.Properties["payload"], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AdditionalProperties == nil || *payload.AdditionalProperties {
		t.Error("payload must set additionalProperties: false")
	}
	for _, f := range New().IDFields() {
		if _, ok := payload.Properties[f.Field]; !ok {
			t.Errorf("payload schema lacks ID field %s", f.Field)
		}
	}
}

func TestRFI_ApplyDefaults_ManagerAndDraft(t *testing.T) {
	f := New()
	out, un := f.ApplyDefaults(ctxFor(42, 2), map[string]any{}, nil, managersWith([]int64{42}, 42))
	if len(un) != 0 {
		t.Errorf("unresolved = %+v", un)
	}
	if out["rfi_manager_id"] != int64(42) || out["draft"] != true || out["private"] != false {
		t.Errorf("out = %v", out)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"assignee_ids":[]`) || !strings.Contains(string(b), `"distribution_ids":[]`) {
		t.Errorf("empty arrays not defaulted: %s", b)
	}

	out, _ = f.ApplyDefaults(ctxFor(42, 5), map[string]any{}, nil, managersWith([]int64{42}, 42))
	if out["draft"] != false {
		t.Errorf("SubmittedCount 5: draft = %v, want false", out["draft"])
	}

	// Values already set are kept, and the input is not mutated.
	in := map[string]any{"rfi_manager_id": float64(7), "draft": false, "private": true}
	out, _ = f.ApplyDefaults(ctxFor(42, 0), in, nil, managersWith([]int64{42}, 42))
	if out["rfi_manager_id"] != float64(7) || out["draft"] != false || out["private"] != true {
		t.Errorf("explicit values overridden: %v", out)
	}
	if _, ok := in["assignee_ids"]; ok {
		t.Error("input payload mutated")
	}
}

// The RFI manager defaults to the speaker only when Procore lists them as a
// potential RFI manager; the project directory does not decide it.
func TestApplyDefaults_RFIManagerFromPotentialManagers(t *testing.T) {
	out, un := New().ApplyDefaults(ctxFor(42, 0), map[string]any{}, nil, managersWith([]int64{1, 2}, 7, 42))
	if len(un) != 0 {
		t.Errorf("unresolved = %+v, want none", un)
	}
	if out["rfi_manager_id"] != int64(42) {
		t.Errorf("rfi_manager_id = %v, want 42 from the RFI managers list", out["rfi_manager_id"])
	}
}

// A speaker in the directory who may not manage RFIs leaves the field for
// the reviewer, offered Procore's potential managers.
func TestApplyDefaults_UserNotManager_Unresolved(t *testing.T) {
	sets := managersWith([]int64{42}, 7, 8)
	out, un := New().ApplyDefaults(ctxFor(42, 0), map[string]any{}, nil, sets)
	if _, ok := out["rfi_manager_id"]; ok {
		t.Errorf("rfi_manager_id = %v, want unset for a user who is not an RFI manager", out["rfi_manager_id"])
	}
	if len(un) != 1 || un[0].Field != "rfi_manager_id" || un[0].Reason != pipeline.ReasonNotFound ||
		un[0].Message != "You are not an RFI manager on this project" {
		t.Fatalf("unresolved = %+v", un)
	}
	if want := sets[pipeline.KindRFIManagers].Rows; !reflect.DeepEqual(un[0].Candidates, want) {
		t.Errorf("candidates = %+v, want the RFI managers %+v", un[0].Candidates, want)
	}
	if b := mustJSON(t, out); !strings.Contains(b, `"assignee_ids":[]`) {
		t.Errorf("assignee default changed: %s", b)
	}

	// With no managers list (the user may not read it), the candidates are
	// empty, not null.
	_, un = New().ApplyDefaults(ctxFor(42, 0), map[string]any{}, nil, usersWith(42))
	if len(un) != 1 || un[0].Field != "rfi_manager_id" || un[0].Candidates == nil || len(un[0].Candidates) != 0 {
		t.Errorf("no managers list: unresolved = %+v", un)
	}
}

func TestRFI_RFIManagerKind(t *testing.T) {
	f := New()
	var kind pipeline.LookupKind
	for _, idf := range f.IDFields() {
		if idf.Field == "rfi_manager_id" {
			kind = idf.Kind
		}
	}
	if kind != pipeline.KindRFIManagers {
		t.Errorf("rfi_manager_id kind = %q, want %q", kind, pipeline.KindRFIManagers)
	}
	if !slices.Contains(f.LookupKinds(), pipeline.KindRFIManagers) {
		t.Errorf("LookupKinds = %v, want rfi_managers warmed", f.LookupKinds())
	}
}

func TestRFI_ApplyDefaults_SkipsManagerWhenUnresolved(t *testing.T) {
	in := []pipeline.Unresolved{{Field: "rfi_manager_id", Reason: pipeline.ReasonAmbiguous,
		Candidates: []pipeline.LookupRow{{ID: 42}, {ID: 43}}}}
	out, un := New().ApplyDefaults(ctxFor(42, 0), map[string]any{}, in, usersWith(42, 43))
	if _, ok := out["rfi_manager_id"]; ok {
		t.Errorf("rfi_manager_id = %v, want unset while unresolved", out["rfi_manager_id"])
	}
	if len(un) != 0 {
		t.Errorf("added unresolved = %+v, want none", un)
	}
}

func TestRFI_PayloadFields_MatchSchema(t *testing.T) {
	f := New()
	got := map[string]bool{}
	for _, p := range f.PayloadFields() {
		got[p] = true
	}
	props := OutputSchema["properties"].(map[string]any)["payload"].(map[string]any)["properties"].(map[string]any)
	if len(got) != len(props) {
		t.Errorf("PayloadFields = %v, schema has %d properties", f.PayloadFields(), len(props))
	}
	for k := range props {
		if !got[k] {
			t.Errorf("PayloadFields lacks %s", k)
		}
	}
	subject := props["subject"].(map[string]any)
	if subject["maxLength"] != 100 {
		t.Errorf("subject maxLength = %v, want 100", subject["maxLength"])
	}
}

func TestRFI_QuestionBody_AppendsFieldNotes(t *testing.T) {
	f := New()
	out, _ := f.ApplyDefaults(ctxFor(42, 0), map[string]any{"question_body": "Why?"}, nil, usersWith(42))
	body, _ := out["question_body"].(string)
	if !strings.HasSuffix(body, "Field notes:\num why") || body != "Why?\n\nField notes:\num why" {
		t.Errorf("question_body = %q", body)
	}

	already := "Why?\n\nField notes:\num why"
	out, _ = f.ApplyDefaults(ctxFor(42, 0), map[string]any{"question_body": already}, nil, usersWith(42))
	if out["question_body"] != already {
		t.Errorf("Field notes appended twice: %q", out["question_body"])
	}
}

func TestRFI_Prompt_LoadsWithVersion(t *testing.T) {
	p := New().Prompt()
	if len(p.Version) != 12 {
		t.Errorf("Version = %q, want 12 hex digits", p.Version)
	}
	for _, s := range []string{"Field notes:", "submit_draft", "unresolved", "yes_unknown", "YYYY-MM-DD", "Never use `tbd`"} {
		if !strings.Contains(p.Text, s) {
			t.Errorf("prompt lacks %q", s)
		}
	}
}

func TestRFI_Metadata(t *testing.T) {
	f := New()
	if f.Name() != "rfi" || len(f.LookupKinds()) != 7 || len(f.IDFields()) != 7 {
		t.Errorf("name %q, kinds %d, id fields %d", f.Name(), len(f.LookupKinds()), len(f.IDFields()))
	}
	if d := f.DateFields(); len(d) != 1 || d[0] != "due_date" {
		t.Errorf("DateFields = %v", d)
	}
}

func TestRFI_UserMessage(t *testing.T) {
	msg := New().UserMessage(ctxFor(42, 0), "the duct hits the beam")
	for _, s := range []string{"Tower", "America/Chicago", "2026-10-24", "Sam Super", "42", "Transcript:\nthe duct hits the beam"} {
		if !strings.Contains(msg, s) {
			t.Errorf("user message lacks %q:\n%s", s, msg)
		}
	}
}
