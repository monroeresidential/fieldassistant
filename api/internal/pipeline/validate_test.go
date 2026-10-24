package pipeline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
)

// testFeature is a minimal Feature for exercising Validate.
type testFeature struct{}

func (testFeature) Name() string              { return "test" }
func (testFeature) LookupKinds() []LookupKind { return AllKinds }
func (testFeature) IDFields() []IDField {
	return []IDField{
		{Field: "assignee_ids", Kind: KindUsers, Multi: true},
		{Field: "rfi_manager_id", Kind: KindUsers},
		{Field: "location_id", Kind: KindLocations},
	}
}
func (testFeature) DateFields() []string { return []string{"due_date"} }
func (testFeature) PayloadFields() []string {
	return []string{"subject", "assignee_ids", "rfi_manager_id", "location_id", "drawing_number", "due_date"}
}
func (testFeature) RequiredFields() []string {
	return []string{"subject", "assignee_ids", "rfi_manager_id"}
}
func (testFeature) Prompt() llm.Prompt                        { return llm.Prompt{} }
func (testFeature) OutputSchema() map[string]any              { return nil }
func (testFeature) UserMessage(ExtractContext, string) string { return "" }
func (testFeature) ApplyDefaults(_ ExtractContext, p map[string]any, _ []Unresolved, _ map[LookupKind]LookupSet) (map[string]any, []Unresolved) {
	return p, nil
}
func (testFeature) Normalize(p map[string]any, u []Unresolved) (map[string]any, []Unresolved) {
	return p, u
}

var (
	testToday = time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	testLoc   = time.UTC
)

func testLookups() map[LookupKind]LookupSet {
	return map[LookupKind]LookupSet{
		KindUsers: {Kind: KindUsers, Rows: []LookupRow{
			{ID: 11, Label: "Ana Diaz", Extra: map[string]string{"role": "Architect"}},
			{ID: 12, Label: "Ana Diaz-Moreno"},
		}},
		KindLocations: {Kind: KindLocations, Rows: []LookupRow{{ID: 7, Label: "Level 3"}}},
	}
}

// decode builds a payload the way the extractor does: from JSON, so numbers
// arrive as float64.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func findUnresolved(us []Unresolved, field string) (Unresolved, int) {
	var first Unresolved
	n := 0
	for _, u := range us {
		if u.Field == field {
			if n == 0 {
				first = u
			}
			n++
		}
	}
	return first, n
}

func TestValidate_UnknownUserID_Demoted(t *testing.T) {
	p := decode(t, `{"assignee_ids":[11,99]}`)
	out, un := Validate(testFeature{}, p, nil, testLookups(), testToday, testLoc)

	ids, ok := out["assignee_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != float64(11) {
		t.Fatalf("assignee_ids = %#v, want [11]", out["assignee_ids"])
	}
	u, n := findUnresolved(un, "assignee_ids")
	if n != 1 || u.Reason != ReasonNotInLookup {
		t.Fatalf("unresolved = %+v, want one assignee_ids/not_in_lookup", un)
	}
	if u.Candidates == nil {
		t.Error("Candidates is nil; want empty slice")
	}
	if in := p["assignee_ids"].([]any); len(in) != 2 {
		t.Errorf("input payload mutated: %v", in)
	}
}

func TestValidate_AllMultiIDsUnknown_StaysEmptyArray(t *testing.T) {
	p := decode(t, `{"assignee_ids":[98,99]}`)
	out, _ := Validate(testFeature{}, p, nil, testLookups(), testToday, testLoc)
	b, _ := json.Marshal(out["assignee_ids"])
	if string(b) != "[]" {
		t.Errorf("assignee_ids = %s, want []", b)
	}
}

func TestValidate_SingleUnknownID_RemovedAndFlagged(t *testing.T) {
	p := decode(t, `{"location_id":5}`)
	out, un := Validate(testFeature{}, p, nil, testLookups(), testToday, testLoc)
	if _, ok := out["location_id"]; ok {
		t.Errorf("location_id still present: %v", out["location_id"])
	}
	if u, n := findUnresolved(un, "location_id"); n != 1 || u.Reason != ReasonNotInLookup {
		t.Errorf("unresolved = %+v, want one location_id/not_in_lookup", un)
	}
}

func TestValidate_DropsCandidatesNotInLookup(t *testing.T) {
	in := []Unresolved{{
		Field:      "rfi_manager_id",
		Reason:     ReasonAmbiguous,
		Candidates: []LookupRow{{ID: 11, Label: "Ana"}, {ID: 404, Label: "Invented"}, {ID: 12, Label: "Ana D"}},
	}}
	_, un := Validate(testFeature{}, map[string]any{}, in, testLookups(), testToday, testLoc)
	u, n := findUnresolved(un, "rfi_manager_id")
	if n != 1 {
		t.Fatalf("unresolved = %+v", un)
	}
	if len(u.Candidates) != 2 || u.Candidates[0].ID != 11 || u.Candidates[1].ID != 12 {
		t.Fatalf("candidates = %+v, want ids 11 and 12", u.Candidates)
	}
	// Model-supplied labels are replaced with the cached rows.
	if u.Candidates[0].Label != "Ana Diaz" || u.Candidates[0].Extra["role"] != "Architect" {
		t.Errorf("candidate 0 = %+v, want the cached row", u.Candidates[0])
	}
	if in[0].Candidates[1].ID != 404 {
		t.Error("input unresolved mutated")
	}
}

func TestValidate_UnresolvedSingleField_RemovedFromPayload_AndDeduped(t *testing.T) {
	p := decode(t, `{"rfi_manager_id":11}`)
	in := []Unresolved{
		{Field: "rfi_manager_id", Reason: ReasonAmbiguous},
		{Field: "rfi_manager_id", Reason: ReasonNotFound},
	}
	out, un := Validate(testFeature{}, p, in, testLookups(), testToday, testLoc)
	if _, ok := out["rfi_manager_id"]; ok {
		t.Error("rfi_manager_id present while unresolved")
	}
	u, n := findUnresolved(un, "rfi_manager_id")
	if n != 1 || u.Reason != ReasonAmbiguous {
		t.Errorf("unresolved = %+v, want one rfi_manager_id/ambiguous (first wins)", un)
	}
	if u.Candidates == nil {
		t.Error("Candidates is nil; want empty slice")
	}
}

func TestValidate_DatePastOrMalformed_Flagged(t *testing.T) {
	for _, d := range []any{"yesterday", "2026-10-23", "10/30/2026", float64(20261030)} {
		p := map[string]any{"due_date": d}
		out, un := Validate(testFeature{}, p, nil, testLookups(), testToday, testLoc)
		if _, ok := out["due_date"]; ok {
			t.Errorf("%v: due_date still present", d)
		}
		if u, n := findUnresolved(un, "due_date"); n != 1 || u.Reason != ReasonDateUnparseable {
			t.Errorf("%v: unresolved = %+v, want one due_date/date_unparseable", d, un)
		}
	}
}

func TestValidate_DateTodayInProjectTimezone_Kept(t *testing.T) {
	// 03:00 UTC on the 25th is still the 24th in Chicago.
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip(err)
	}
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	out, un := Validate(testFeature{}, map[string]any{"due_date": "2026-10-24"}, nil, testLookups(), now, loc)
	if out["due_date"] != "2026-10-24" || len(un) != 0 {
		t.Errorf("due_date = %v, unresolved = %+v; want kept with no flags", out["due_date"], un)
	}
}

func TestValidate_KeepsKnownIDs_NoFlag(t *testing.T) {
	p := decode(t, `{"assignee_ids":[11,12],"rfi_manager_id":11,"location_id":7,"due_date":"2026-11-01","subject":"S"}`)
	out, un := Validate(testFeature{}, p, nil, testLookups(), testToday, testLoc)
	if len(un) != 0 {
		t.Errorf("unresolved = %+v, want none", un)
	}
	if len(out["assignee_ids"].([]any)) != 2 || out["rfi_manager_id"] != float64(11) ||
		out["location_id"] != float64(7) || out["due_date"] != "2026-11-01" || out["subject"] != "S" {
		t.Errorf("payload changed: %v", out)
	}
}

func TestValidate_NonIDField_CandidatesDropped(t *testing.T) {
	p := map[string]any{"drawing_number": "M-301"}
	in := []Unresolved{{Field: "drawing_number", Reason: ReasonAmbiguous, Candidates: []LookupRow{{ID: 999, Label: "Invented"}}}}
	out, un := Validate(testFeature{}, p, in, testLookups(), testToday, testLoc)
	u, n := findUnresolved(un, "drawing_number")
	if n != 1 {
		t.Fatalf("unresolved = %+v, want the drawing_number entry kept", un)
	}
	if u.Candidates == nil || len(u.Candidates) != 0 {
		t.Errorf("candidates = %#v, want empty non-nil", u.Candidates)
	}
	if out["drawing_number"] != "M-301" {
		t.Errorf("drawing_number = %v, want kept", out["drawing_number"])
	}
}

func TestValidate_UnknownField_Dropped(t *testing.T) {
	in := []Unresolved{{Field: "assignee", Reason: ReasonAmbiguous, Candidates: []LookupRow{{ID: 11}}}}
	_, un := Validate(testFeature{}, map[string]any{}, in, testLookups(), testToday, testLoc)
	if len(un) != 0 {
		t.Errorf("unresolved = %+v, want the unknown field dropped", un)
	}
}

func TestValidate_NonIntegerID_Removed(t *testing.T) {
	out, un := Validate(testFeature{}, map[string]any{"location_id": 7.5}, nil, testLookups(), testToday, testLoc)
	if _, ok := out["location_id"]; ok {
		t.Error("fractional location_id kept")
	}
	if _, n := findUnresolved(un, "location_id"); n != 1 {
		t.Errorf("unresolved = %+v", un)
	}
}
