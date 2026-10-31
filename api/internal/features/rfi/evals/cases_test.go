// Package evals scores the RFI extraction against a fixed set of dictated
// cases. The live run is in evals_test.go behind the evals build tag; this
// file holds the case format, the scoring and the untagged checks that keep
// the case files well formed.
package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/features/rfi"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

// evalCase is one cases/*.json file.
type evalCase struct {
	File       string `json:"-"`
	Name       string `json:"name"`
	Transcript string `json:"transcript"`
	// Today is the project-local date the extraction runs on, YYYY-MM-DD.
	Today    string `json:"today"`
	TimeZone string `json:"time_zone"`
	User     struct {
		ProcoreID int64  `json:"procore_id"`
		Name      string `json:"name"`
	} `json:"user"`
	Fixture map[pipeline.LookupKind][]pipeline.LookupRow `json:"fixture"`
	Expect  expect                                       `json:"expect"`
}

// expect is what a case requires of the validated draft. When AnyOf is set,
// each entry is merged with the top-level fields and the case scores against
// the alternative that matches best.
type expect struct {
	// Payload keys must equal these values by JSON equality.
	Payload map[string]any `json:"payload,omitempty"`
	// Absent keys must be missing or empty.
	Absent []string `json:"absent,omitempty"`
	// Unresolved fields must be flagged, with the reason and candidates
	// given when they are set.
	Unresolved []expectUnresolved `json:"unresolved,omitempty"`
	// ForbidIDs are ID fields that must hold no ID at all; any ID there
	// counts as a hallucination.
	ForbidIDs []string `json:"forbid_ids,omitempty"`
	AnyOf     []expect `json:"any_of,omitempty"`
}

type expectUnresolved struct {
	Field        string  `json:"field"`
	Reason       string  `json:"reason,omitempty"`
	CandidateIDs []int64 `json:"candidate_ids,omitempty"`
}

// alternatives returns the expectation blocks a case may satisfy.
func (e expect) alternatives() []expect {
	if len(e.AnyOf) == 0 {
		return []expect{e}
	}
	out := make([]expect, 0, len(e.AnyOf))
	for _, alt := range e.AnyOf {
		m := expect{Payload: map[string]any{}, ForbidIDs: e.ForbidIDs}
		for k, v := range e.Payload {
			m.Payload[k] = v
		}
		for k, v := range alt.Payload {
			m.Payload[k] = v
		}
		m.Absent = append(append([]string{}, e.Absent...), alt.Absent...)
		m.Unresolved = append(append([]expectUnresolved{}, e.Unresolved...), alt.Unresolved...)
		out = append(out, m)
	}
	return out
}

// now is noon on the case's date in its time zone.
func (c evalCase) now() (time.Time, error) {
	loc, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return time.Time{}, err
	}
	d, err := time.ParseInLocation("2006-01-02", c.Today, loc)
	if err != nil {
		return time.Time{}, err
	}
	return d.Add(12 * time.Hour), nil
}

func loadCases(t *testing.T) []evalCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("cases", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	cases := make([]evalCase, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var c evalCase
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		c.File = filepath.Base(f)
		cases = append(cases, c)
	}
	return cases
}

// score is one case's result.
type score struct {
	Matched, Expected int
	// Hallucinated lists IDs in the payload that are not in the fixture or
	// sit in a forbidden field.
	Hallucinated []string
	// Precision is flagged fields that were expected over flagged fields;
	// 1 when nothing was flagged.
	Precision float64
	Misses    []string
}

// scoreDraft scores a validated payload and unresolved list against a case.
func scoreDraft(c evalCase, idFields []pipeline.IDField, payloadJSON []byte, unresolved []pipeline.Unresolved) (score, error) {
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return score{}, fmt.Errorf("decode payload: %w", err)
	}
	var s score
	for _, idf := range idFields {
		forbidden := contains(c.Expect.ForbidIDs, idf.Field)
		for _, id := range payloadIDs(payload[idf.Field]) {
			if forbidden || !fixtureHas(c.Fixture[idf.Kind], id) {
				s.Hallucinated = append(s.Hallucinated, fmt.Sprintf("%s=%d", idf.Field, id))
			}
		}
	}

	flagged := map[string]pipeline.Unresolved{}
	for _, u := range unresolved {
		if _, ok := flagged[u.Field]; !ok {
			flagged[u.Field] = u
		}
	}

	chosen := false
	for _, alt := range c.Expect.alternatives() {
		cur := score{Hallucinated: s.Hallucinated}
		check := func(ok bool, miss string) {
			cur.Expected++
			if ok {
				cur.Matched++
			} else {
				cur.Misses = append(cur.Misses, miss)
			}
		}
		for _, k := range sortedKeys(alt.Payload) {
			got, present := payload[k]
			check(present && jsonEqual(got, alt.Payload[k]), fmt.Sprintf("%s: got %s, want %s", k, compact(got, present), compact(alt.Payload[k], true)))
		}
		for _, k := range alt.Absent {
			got, present := payload[k]
			check(!present || isEmpty(got), fmt.Sprintf("%s: got %s, want absent", k, compact(got, present)))
		}
		want := map[string]bool{}
		for _, eu := range alt.Unresolved {
			want[eu.Field] = true
			u, ok := flagged[eu.Field]
			ok = ok && (eu.Reason == "" || u.Reason == eu.Reason)
			if ok && eu.CandidateIDs != nil {
				ok = sameIDs(candidateIDs(u.Candidates), eu.CandidateIDs)
			}
			check(ok, fmt.Sprintf("unresolved %s: got %s, want %+v", eu.Field, compact(flagged[eu.Field], flagged[eu.Field].Field != ""), eu))
		}
		cur.Precision = 1
		if len(flagged) > 0 {
			hit := 0
			for f := range flagged {
				if want[f] {
					hit++
				}
			}
			cur.Precision = float64(hit) / float64(len(flagged))
		}
		if !chosen || better(cur, s) {
			s = cur
			chosen = true
		}
	}
	return s, nil
}

// better prefers more matched fields, then higher precision.
func better(a, b score) bool {
	if a.Matched != b.Matched {
		return a.Matched > b.Matched
	}
	return a.Precision > b.Precision
}

// payloadIDs returns the IDs in a single or multi ID field value.
func payloadIDs(v any) []int64 {
	switch x := v.(type) {
	case float64:
		return []int64{int64(x)}
	case []any:
		var out []int64
		for _, item := range x {
			out = append(out, payloadIDs(item)...)
		}
		return out
	}
	return nil
}

func fixtureHas(rows []pipeline.LookupRow, id int64) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

func candidateIDs(rows []pipeline.LookupRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func sameIDs(a, b []int64) bool {
	a, b = append([]int64{}, a...), append([]int64{}, b...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return reflect.DeepEqual(a, b)
}

// jsonEqual compares after a JSON round trip, so numbers compare as float64
// whatever Go type produced them.
func jsonEqual(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func compact(v any, present bool) string {
	if !present {
		return "absent"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestCases_Parse checks every case file is well formed: the ten cases load,
// the dates and zones parse, the speaker is in the fixture, every field named
// in expect is an RFI payload field, and every ID expect refers to exists in
// the fixture list for that field.
func TestCases_Parse(t *testing.T) {
	f := rfi.New()
	fields := map[string]bool{}
	for _, name := range f.PayloadFields() {
		fields[name] = true
	}
	kinds := map[string]pipeline.LookupKind{}
	for _, idf := range f.IDFields() {
		kinds[idf.Field] = idf.Kind
	}

	cases := loadCases(t)
	if len(cases) != 10 {
		t.Fatalf("got %d case files, want 10", len(cases))
	}
	for _, c := range cases {
		t.Run(c.File, func(t *testing.T) {
			if c.Name == "" || c.Transcript == "" {
				t.Error("name and transcript are required")
			}
			if _, err := c.now(); err != nil {
				t.Errorf("today/time_zone: %v", err)
			}
			for kind := range c.Fixture {
				if _, ok := pipeline.ParseKind(string(kind)); !ok {
					t.Errorf("fixture has unknown kind %q", kind)
				}
			}
			for _, k := range f.LookupKinds() {
				if len(c.Fixture[k]) == 0 {
					t.Errorf("fixture has no %s rows", k)
				}
			}
			if !fixtureHas(c.Fixture[pipeline.KindUsers], c.User.ProcoreID) {
				t.Errorf("speaker %d is not in the users fixture", c.User.ProcoreID)
			}
			if !fixtureHas(c.Fixture[pipeline.KindRFIManagers], c.User.ProcoreID) {
				t.Errorf("speaker %d is not in the rfi_managers fixture, so rfi_manager_id would be flagged", c.User.ProcoreID)
			}
			checkField := func(name string) {
				if !fields[name] {
					t.Errorf("expect names unknown field %q", name)
				}
			}
			checkIDs := func(field string, ids []int64) {
				kind, ok := kinds[field]
				if !ok {
					if len(ids) > 0 {
						t.Errorf("%s is not an ID field but has IDs", field)
					}
					return
				}
				for _, id := range ids {
					if !fixtureHas(c.Fixture[kind], id) {
						t.Errorf("%s: ID %d is not in the %s fixture", field, id, kind)
					}
				}
			}
			for _, alt := range c.Expect.alternatives() {
				for k, v := range alt.Payload {
					checkField(k)
					checkIDs(k, payloadIDs(normalize(v)))
				}
				for _, k := range alt.Absent {
					checkField(k)
				}
				for _, u := range alt.Unresolved {
					checkField(u.Field)
					checkIDs(u.Field, u.CandidateIDs)
				}
				for _, k := range alt.ForbidIDs {
					if _, ok := kinds[k]; !ok {
						t.Errorf("forbid_ids names %q, which is not an ID field", k)
					}
				}
			}
		})
	}
}

// TestScoreDraft checks the scorer on a hand-built draft.
func TestScoreDraft(t *testing.T) {
	c := evalCase{
		Fixture: map[pipeline.LookupKind][]pipeline.LookupRow{
			pipeline.KindUsers:     {{ID: 11}, {ID: 12}},
			pipeline.KindLocations: {{ID: 202}},
		},
		Expect: expect{
			Payload:   map[string]any{"assignee_ids": []any{11}},
			Absent:    []string{"due_date"},
			ForbidIDs: []string{"distribution_ids"},
			AnyOf: []expect{
				{Payload: map[string]any{"location_id": 202}},
				{Absent: []string{"location_id"}, Unresolved: []expectUnresolved{{Field: "location_id", Reason: "not_found"}}},
			},
		},
	}
	ids := rfi.New().IDFields()

	s, err := scoreDraft(c, ids, []byte(`{"assignee_ids":[11],"location_id":202,"due_date":""}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Matched != 3 || s.Expected != 3 || len(s.Hallucinated) != 0 || s.Precision != 1 {
		t.Errorf("first alternative: %+v", s)
	}

	s, _ = scoreDraft(c, ids, []byte(`{"assignee_ids":[11]}`), []pipeline.Unresolved{{Field: "location_id", Reason: "not_found"}, {Field: "subject", Reason: "ambiguous"}})
	if s.Matched != 4 || s.Expected != 4 || s.Precision != 0.5 {
		t.Errorf("second alternative: %+v", s)
	}

	s, _ = scoreDraft(c, ids, []byte(`{"assignee_ids":[99],"distribution_ids":[12],"due_date":"2026-11-13"}`), nil)
	if len(s.Hallucinated) != 2 || s.Matched != 1 || s.Expected != 4 {
		t.Errorf("hallucinations and misses: %+v", s)
	}
}
