package rfi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

var _ pipeline.Feature = (*Feature)(nil)

func usersWith(ids ...int64) map[pipeline.LookupKind]pipeline.LookupSet {
	rows := []pipeline.LookupRow{}
	for _, id := range ids {
		rows = append(rows, pipeline.LookupRow{ID: id, Label: "U"})
	}
	return map[pipeline.LookupKind]pipeline.LookupSet{pipeline.KindUsers: {Kind: pipeline.KindUsers, Rows: rows}}
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
	out, un := f.ApplyDefaults(ctxFor(42, 2), map[string]any{}, nil, usersWith(42))
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

	out, _ = f.ApplyDefaults(ctxFor(42, 5), map[string]any{}, nil, usersWith(42))
	if out["draft"] != false {
		t.Errorf("SubmittedCount 5: draft = %v, want false", out["draft"])
	}

	// Values already set are kept, and the input is not mutated.
	in := map[string]any{"rfi_manager_id": float64(7), "draft": false, "private": true}
	out, _ = f.ApplyDefaults(ctxFor(42, 0), in, nil, usersWith(42))
	if out["rfi_manager_id"] != float64(7) || out["draft"] != false || out["private"] != true {
		t.Errorf("explicit values overridden: %v", out)
	}
	if _, ok := in["assignee_ids"]; ok {
		t.Error("input payload mutated")
	}
}

func TestRFI_ApplyDefaults_ManagerNotInDirectory_Unresolved(t *testing.T) {
	out, un := New().ApplyDefaults(ctxFor(42, 0), map[string]any{}, nil, usersWith(1, 2))
	if _, ok := out["rfi_manager_id"]; ok {
		t.Error("rfi_manager_id set for a user not in the directory")
	}
	if len(un) != 1 || un[0].Field != "rfi_manager_id" || un[0].Reason != pipeline.ReasonNotFound ||
		un[0].Message != "You are not in this project's directory" || un[0].Candidates == nil {
		t.Errorf("unresolved = %+v", un)
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
	if f.Name() != "rfi" || len(f.LookupKinds()) != 6 || len(f.IDFields()) != 7 {
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
