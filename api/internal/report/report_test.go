package report_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/report"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

const (
	companyID = int64(1)
	projectID = int64(2)
)

var (
	from = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	to   = from.AddDate(0, 0, 7)
	week = report.Window{From: from, To: to}
)

// fixture seeds rows straight into the tables the report reads.
type fixture struct {
	t  *testing.T
	st *store.Store
}

func newFixture(t *testing.T) fixture {
	return fixture{t: t, st: storetest.New(t)}
}

func (f fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f fixture) user(procoreID int64, name string) uuid.UUID {
	f.t.Helper()
	u, err := f.st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{
		ProcoreUserID: procoreID, Email: fmt.Sprintf("user%d@example.com", procoreID), Name: name,
	})
	if err != nil {
		f.t.Fatalf("upsert user: %v", err)
	}
	return uuid.UUID(u.ID.Bytes)
}

// draftRow is a drafts row; zero values are left null where the column
// allows it.
type draftRow struct {
	user        uuid.UUID
	status      string
	transcript  string
	created     time.Time
	llm, final  string
	extractMS   int
	recordID    int64
	errorCode   string
	recordTitle string
}

func (f fixture) draft(r draftRow) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	if r.status == "" {
		r.status = "extracted"
	}
	if r.transcript == "" {
		r.transcript = "transcript " + id.String()[:8]
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var ms, rec any
	if r.extractMS > 0 {
		ms = r.extractMS
	}
	if r.recordID > 0 {
		rec = r.recordID
	}
	f.exec(`insert into drafts (id, company_id, project_id, user_id, feature, status, transcript, captured_at,
		llm_output, user_payload, extract_ms, procore_record_id, error_code, created_at, updated_at)
		values ($1, $2, $3, $4, 'rfi', $5, $6, $7, $8::jsonb, $9::jsonb, $10, $11, $12, $7, $7)`,
		id, companyID, projectID, r.user, r.status, r.transcript, r.created,
		nullable(r.llm), nullable(r.final), ms, rec, nullable(r.errorCode))
	return id
}

func (f fixture) audit(draftID uuid.UUID, kind string, at time.Time, payload, response string) {
	f.t.Helper()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	f.exec(`insert into audit_events (company_id, draft_id, kind, payload, procore_response, created_at)
		values ($1, $2, $3, $4::jsonb, $5::jsonb, $6)`,
		companyID, draftID, kind, nullable(payload), nullable(response), at)
}

func (f fixture) attachment(draftID uuid.UUID, state, code string) {
	f.t.Helper()
	var c any
	if code != "" {
		c = code
	}
	f.exec(`insert into attachments (id, company_id, draft_id, filename, content_type, bytes, state, error_code)
		values ($1, $2, $3, 'p.jpg', 'image/jpeg', 10, $4, $5)`,
		uuid.New(), companyID, draftID, state, c)
}

func (f fixture) mirrorRFI(owner uuid.UUID, procoreID int64, subject string, creator int64) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"id": procoreID, "subject": subject, "created_by": map[string]any{"id": creator, "name": "x"}})
	f.exec(`insert into user_records (company_id, user_id, project_id, kind, procore_id, procore_updated_at, body, body_hash, change_seq)
		values ($1, $2, $3, 'rfi', $4, now(), $5, 'h', nextval('records_change_seq'))`,
		companyID, owner, projectID, procoreID, body)
}

func (f fixture) build() report.Summary {
	f.t.Helper()
	s, err := report.Build(context.Background(), f.st.Q, week)
	if err != nil {
		f.t.Fatalf("Build: %v", err)
	}
	return s
}

func at(h int) time.Time { return from.Add(time.Duration(h) * time.Hour) }

func TestPilotReport_CorrectionsCountFieldsNotSaves(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	llm := `{"subject":"Beam","question_body":"Is it level?","due_date":"2026-11-13"}`
	final := `{"subject":"Beam at grid C","question_body":"Is the beam level?","due_date":"2026-11-13"}`
	id := f.draft(draftRow{user: u, status: "submitted", created: at(1), llm: llm, final: final, recordID: 501})
	// The subject was saved four times and the question once, with a
	// system re-save on top; only the two fields count.
	subjects := []string{"Beam 1", "Beam 2", "Beam 3", "Beam at grid C"}
	before := llm
	for i, s := range subjects {
		after := fmt.Sprintf(`{"subject":%q,"question_body":"Is it level?","due_date":"2026-11-13"}`, s)
		f.audit(id, "edited", at(1).Add(time.Duration(i+1)*time.Minute), fmt.Sprintf(`{"before":%s,"after":%s}`, before, after), "")
		before = after
	}
	f.audit(id, "edited", at(1).Add(10*time.Minute), fmt.Sprintf(`{"before":%s,"after":%s}`, before, final), "")
	f.audit(id, "edited", at(1).Add(11*time.Minute), fmt.Sprintf(`{"before":%s,"after":%s}`, final, final), "")

	if got := report.Corrections(json.RawMessage(llm), json.RawMessage(final)); !reflect.DeepEqual(got, []string{"question_body", "subject"}) {
		t.Errorf("Corrections = %v, want [question_body subject]", got)
	}
	s := f.build()
	if got := s.Corrections.PerDraft[id]; got != 2 {
		t.Errorf("PerDraft = %d, want 2", got)
	}
	if s.Corrections.DraftsOverTwo != 0 {
		t.Errorf("DraftsOverTwo = %d, want 0", s.Corrections.DraftsOverTwo)
	}
	if s.Corrections.Histogram[2] != 1 {
		t.Errorf("Histogram = %v, want {2:1}", s.Corrections.Histogram)
	}
}

func TestPilotReport_CorrectionsIncludeFilledEmpties(t *testing.T) {
	llm := `{"subject":"S","due_date":"","location_id":null,"assignee_ids":[],"drawing_ids":[601],
		"schedule_impact":{"status":"no"},"question_body":"Q"}`
	final := `{"subject":"S","due_date":"2026-11-13","location_id":202,"assignee_ids":[],"drawing_ids":[],
		"schedule_impact":{"status":"no","value":null},"cost_impact":{"status":"yes_known","value":"2000"},
		"question_body":"Q","reference":""}`
	want := []string{"cost_impact", "drawing_ids", "due_date", "location_id"}
	if got := report.Corrections(json.RawMessage(llm), json.RawMessage(final)); !reflect.DeepEqual(got, want) {
		t.Errorf("Corrections = %v, want %v", got, want)
	}
	// Nested impacts compare as values: a changed value is one correction.
	a := `{"cost_impact":{"status":"yes_known","value":"1500"}}`
	b := `{"cost_impact":{"status":"yes_known","value":"2000"}}`
	if got := report.Corrections(json.RawMessage(a), json.RawMessage(b)); !reflect.DeepEqual(got, []string{"cost_impact"}) {
		t.Errorf("impact value change = %v, want [cost_impact]", got)
	}
	if got := report.Corrections(json.RawMessage(a), json.RawMessage(a)); len(got) != 0 {
		t.Errorf("unchanged = %v, want none", got)
	}
	// Reordering a list of IDs is not a correction; changing it is.
	ids1 := `{"assignee_ids":[11,12],"distribution_ids":["a","b"]}`
	ids2 := `{"assignee_ids":[12,11],"distribution_ids":["b","a"]}`
	if got := report.Corrections(json.RawMessage(ids1), json.RawMessage(ids2)); len(got) != 0 {
		t.Errorf("reordered lists = %v, want none", got)
	}
	if got := report.Corrections(json.RawMessage(ids1), json.RawMessage(`{"assignee_ids":[11,13],"distribution_ids":["a","b"]}`)); !reflect.DeepEqual(got, []string{"assignee_ids"}) {
		t.Errorf("changed list = %v, want [assignee_ids]", got)
	}

	f := newFixture(t)
	u := f.user(42, "Pat Field")
	id := f.draft(draftRow{user: u, status: "submitted", created: at(2), llm: llm, final: final, recordID: 701})
	// A submitted draft nobody edited has no corrections: its final payload
	// is the extraction.
	untouched := f.draft(draftRow{user: u, status: "submitted", created: at(3), llm: llm, recordID: 702})
	s := f.build()
	if got := s.Corrections.PerDraft[id]; got != 4 {
		t.Errorf("PerDraft = %d, want 4", got)
	}
	if got, ok := s.Corrections.PerDraft[untouched]; !ok || got != 0 {
		t.Errorf("untouched PerDraft = %d (present %v), want 0", got, ok)
	}
	if s.Corrections.DraftsOverTwo != 1 {
		t.Errorf("DraftsOverTwo = %d, want 1", s.Corrections.DraftsOverTwo)
	}
}

func TestPilotReport_DuplicatesFromMultipleSubmittedEvents(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	other := f.user(43, "Mike Ross")
	twice := f.draft(draftRow{user: u, status: "submitted", created: at(1), llm: `{"subject":"Twice"}`, recordID: 501})
	f.audit(twice, "attempt", at(1).Add(time.Minute), `{"attempt":1,"subject":"Twice"}`, "")
	f.audit(twice, "submitted", at(1).Add(2*time.Minute), "", `{"id":501}`)
	f.audit(twice, "submitted", at(1).Add(3*time.Minute), "", `{"id":501}`)
	once := f.draft(draftRow{user: u, status: "submitted", created: at(2), llm: `{"subject":"Once"}`, final: `{"subject":"Column"}`, recordID: 502})
	f.audit(once, "attempt", at(2).Add(time.Minute), `{"attempt":1,"subject":"Column first"}`, "")
	f.audit(once, "submitted", at(2).Add(2*time.Minute), "", `{"id":502}`)
	// 504 belongs to another draft (renamed in Procore since), so it is
	// that draft's RFI, not a duplicate.
	sibling := f.draft(draftRow{user: u, status: "submitted", created: at(3), llm: `{"subject":"Sibling"}`, recordID: 504})

	// The mirror: each draft's own RFI, a stray RFI with the draft's subject
	// by its creator (the duplicate), one with an earlier attempt's subject,
	// one by someone else, one deleted, and one owned by another draft.
	f.mirrorRFI(u, 501, "Twice", 42)
	f.mirrorRFI(u, 502, "Column", 42)
	f.mirrorRFI(other, 502, "Column", 42) // same RFI in another user's mirror
	f.mirrorRFI(u, 503, "Column", 42)
	f.mirrorRFI(other, 503, "Column", 42)
	f.mirrorRFI(u, 505, "Column first", 42)
	f.mirrorRFI(u, 506, "Column", 43)
	f.mirrorRFI(u, 507, "Column", 42)
	f.exec(`update user_records set deleted_at = now() where procore_id = 507`)
	f.mirrorRFI(u, 504, "Column", 42)

	s := f.build()
	if !reflect.DeepEqual(s.Duplicates.MultipleSubmitted, []uuid.UUID{twice}) {
		t.Errorf("MultipleSubmitted = %v, want [%v]", s.Duplicates.MultipleSubmitted, twice)
	}
	if len(s.Duplicates.UnknownAttempts) != 0 {
		t.Errorf("UnknownAttempts = %v, want none", s.Duplicates.UnknownAttempts)
	}
	var got []int64
	for _, m := range s.Duplicates.MirrorMatches {
		if m.DraftID != once || m.DraftRecordID != 502 {
			t.Errorf("match %+v belongs to the wrong draft", m)
		}
		got = append(got, m.ProcoreID)
	}
	if !reflect.DeepEqual(got, []int64{503, 505}) {
		t.Errorf("MirrorMatches = %v, want [503 505] (sibling %v owns 504)", got, sibling)
	}
}

func TestPilotReport_UnknownAttemptListed(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	d := func(h int, events ...[3]string) uuid.UUID {
		id := f.draft(draftRow{user: u, status: "submitting", created: at(h), llm: `{"subject":"S"}`})
		for i, ev := range events {
			when := at(h).Add(time.Duration(i+1) * time.Minute)
			if ev[0] == "late" {
				when = to.Add(time.Hour)
				ev[0] = "attempt"
			}
			f.audit(id, ev[0], when, ev[1], ev[2])
		}
		return id
	}
	attempt := [3]string{"attempt", `{"attempt":1,"subject":"S"}`, ""}
	open := d(1, attempt)
	d(2, attempt, [3]string{"submitted", "", `{"id":1}`})
	d(3, attempt, [3]string{"failed", "", `{"error":"422","rejected":true}`})
	d(4, attempt, [3]string{"failed", "", `{"error":"check Procore","closes_unknown_run":true}`})
	retried := d(5, attempt, [3]string{"failed", "", `{"error":"timeout","unknown_outcome":true}`})
	// Rejected, then posted again with no answer: the second POST is open.
	again := d(6, attempt, [3]string{"failed", "", `{"rejected":true}`}, attempt)
	// Submitted, then a new run's attempt is still open.
	reopened := d(7, attempt, [3]string{"submitted", "", `{"id":2}`}, attempt)
	// A failure before any POST leaves nothing unknown.
	d(8, [3]string{"failed", "", `{"error":"x","before_post":true}`})
	// An attempt after the window's end is not part of this week.
	d(9, [3]string{"late", `{"attempt":1}`, ""})

	s := f.build()
	want := []uuid.UUID{open, retried, again, reopened}
	if !reflect.DeepEqual(s.Duplicates.UnknownAttempts, want) {
		t.Errorf("UnknownAttempts = %v, want %v", s.Duplicates.UnknownAttempts, want)
	}
}

func TestPilotReport_ExtractionPercentiles(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	for i := 10; i >= 1; i-- {
		f.draft(draftRow{user: u, created: at(i), llm: `{"subject":"S"}`, extractMS: i * 100})
	}
	f.draft(draftRow{user: u, created: at(11)}) // not built: no extract_ms
	s := f.build()
	if s.Extraction.N != 10 || s.Extraction.P50 != 500*time.Millisecond || s.Extraction.P95 != time.Second {
		t.Errorf("Extraction = %+v, want n 10, p50 500ms, p95 1s", s.Extraction)
	}
	if s.Drafts.Created != 11 || s.Drafts.Built != 10 {
		t.Errorf("Drafts = %+v, want created 11, built 10", s.Drafts)
	}
}

func TestPilotReport_PhotosByCode(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	a := f.draft(draftRow{user: u, status: "submitted", created: at(1), llm: `{}`, recordID: 1})
	b := f.draft(draftRow{user: u, status: "failed", created: at(2), llm: `{}`, errorCode: "procore_validation"})
	outside := f.draft(draftRow{user: u, created: from.Add(-time.Hour), llm: `{}`})
	f.attachment(a, "uploaded", "")
	f.attachment(a, "uploaded", "")
	f.attachment(b, "failed", "procore_refused")
	f.attachment(b, "failed", "procore_refused")
	f.attachment(b, "failed", "expired")
	f.attachment(b, "stored", "")
	f.attachment(outside, "uploaded", "")
	f.attachment(outside, "failed", "size_mismatch")

	s := f.build()
	if s.Photos.Pushed != 2 {
		t.Errorf("Pushed = %d, want 2", s.Photos.Pushed)
	}
	if want := map[string]int{"procore_refused": 2, "expired": 1}; !reflect.DeepEqual(s.Photos.FailedByCode, want) {
		t.Errorf("FailedByCode = %v, want %v", s.Photos.FailedByCode, want)
	}
	if s.Drafts.Submitted != 1 || s.Drafts.Failed != 1 || s.Drafts.FailedByCode["procore_validation"] != 1 {
		t.Errorf("Drafts = %+v", s.Drafts)
	}
}

// corrected returns an extraction and a final payload differing in n fields.
func corrected(n int) (string, string) {
	llm, final := map[string]any{"subject": "S"}, map[string]any{"subject": "S"}
	for i := range n {
		final[fmt.Sprintf("field_%d", i)] = "x"
	}
	a, _ := json.Marshal(llm)
	b, _ := json.Marshal(final)
	return string(a), string(b)
}

func TestPilotReport_WorstFiveOrdered(t *testing.T) {
	f := newFixture(t)
	pat := f.user(42, "Pat Field")
	mike := f.user(43, "Mike Ross")
	ids := map[int]uuid.UUID{}
	for _, n := range []int{3, 0, 6, 1, 5, 2, 4} {
		llm, final := corrected(n)
		u := pat
		if n%2 == 0 {
			u = mike
		}
		ids[n] = f.draft(draftRow{user: u, status: "submitted", created: at(n + 1), llm: llm, final: final, recordID: int64(600 + n)})
	}
	// A tie with the 4-correction draft, created later, ranks after it.
	llm, final := corrected(4)
	tie := f.draft(draftRow{user: pat, status: "submitted", created: at(20), llm: llm, final: final, recordID: 700})
	// A heavily edited draft that was never submitted is not a candidate.
	llm9, final9 := corrected(9)
	f.draft(draftRow{user: pat, status: "reviewed", created: at(21), llm: llm9, final: final9})

	s := f.build()
	want := []uuid.UUID{ids[6], ids[5], ids[4], tie, ids[3]}
	var got []uuid.UUID
	for _, w := range s.Worst {
		got = append(got, w.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Worst = %v, want %v", got, want)
	}
	if w := s.Worst[0]; w.User != "Mike Ross" || len(w.Fields) != 6 || !w.CreatedAt.Equal(at(7)) || w.Transcript == "" {
		t.Errorf("Worst[0] = %+v", w)
	}
	if s.Corrections.DraftsOverTwo != 5 {
		t.Errorf("DraftsOverTwo = %d, want 5", s.Corrections.DraftsOverTwo)
	}
	byName := map[string]report.UserRow{}
	for _, r := range s.PerUser {
		byName[r.Name] = r
	}
	if r := byName["Mike Ross"]; r.Created != 4 || r.Submitted != 4 || r.Corrections != 12 {
		t.Errorf("Mike Ross = %+v, want created 4, submitted 4, corrections 12", r)
	}
	if r := byName["Pat Field"]; r.Created != 5 || r.Submitted != 4 || r.Corrections != 13 {
		t.Errorf("Pat Field = %+v, want created 5, submitted 4, corrections 13", r)
	}
}

var headings = []string{
	"# Pilot summary", "## Window", "## Drafts", "## Corrections", "## Duplicates",
	"## Extraction", "## Photos", "## Per user", "## Worst five",
}

func TestPilotReport_MarkdownHasEverySection(t *testing.T) {
	empty := report.Markdown(report.Summary{Window: week})
	for _, h := range headings {
		if !strings.Contains(empty, h+"\n") {
			t.Errorf("empty summary lacks %q", h)
		}
	}
	if strings.Count(empty, "none") < 5 {
		t.Errorf("empty summary should say none in its empty sections:\n%s", empty)
	}

	f := newFixture(t)
	u := f.user(42, "Pat | Field")
	llm, final := corrected(3)
	worst := f.draft(draftRow{user: u, status: "submitted", created: at(1), llm: llm, final: final,
		extractMS: 4200, recordID: 501, transcript: "The steel column at the roof is twisted."})
	f.audit(worst, "attempt", at(1).Add(time.Minute), `{"attempt":1,"subject":"S"}`, "")
	f.audit(worst, "submitted", at(1).Add(2*time.Minute), "", `{}`)
	f.audit(worst, "submitted", at(1).Add(3*time.Minute), "", `{}`)
	f.mirrorRFI(u, 502, "S", 42)
	failed := f.draft(draftRow{user: u, status: "failed", created: at(2), llm: `{}`, errorCode: "procore_validation"})
	f.audit(failed, "attempt", at(2).Add(time.Minute), `{"attempt":1}`, "")
	f.attachment(worst, "uploaded", "")
	f.attachment(failed, "failed", "procore_refused")

	md := report.Markdown(f.build())
	for _, h := range headings {
		if !strings.Contains(md, h+"\n") {
			t.Errorf("summary lacks %q", h)
		}
	}
	for _, want := range []string{
		"2026-11-03T00:00:00Z", "2026-11-10T00:00:00Z",
		"| Created | 2 |", "| Submitted | 1 |", "procore_validation",
		"Drafts over two corrections: 1 over 1 submitted drafts", "Built drafts not submitted, left out: 1.",
		worst.String(), failed.String(), "502",
		"| p50 | 4.2s |", "| Pushed | 1 |", "procore_refused",
		`Pat \| Field`, "The steel column at the roof is twisted.", "```json", `"field_2": "x"`,
		"evals/pilot/",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "@example.com") {
		t.Error("summary prints an email")
	}
	// Every section heading still appears in order.
	last := -1
	for _, h := range headings {
		i := strings.Index(md, h+"\n")
		if i < last {
			t.Errorf("%q out of order", h)
		}
		last = i
	}
	if !slices.Contains(strings.Split(md, "\n"), "## Worst five") {
		t.Error("worst five heading not on its own line")
	}
}

func TestPilotReport_WindowBounds(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	sub := func(created time.Time, rec int64) uuid.UUID {
		return f.draft(draftRow{user: u, status: "submitted", created: created, llm: `{}`, recordID: rec})
	}
	before := sub(from.Add(-time.Second), 801)
	first := sub(from, 802)
	last := sub(to.Add(-time.Second), 803)
	after := sub(to, 804)

	s := f.build()
	if s.Drafts.Created != 2 {
		t.Errorf("Created = %d, want 2", s.Drafts.Created)
	}
	for _, id := range []uuid.UUID{first, last} {
		if _, ok := s.Corrections.PerDraft[id]; !ok {
			t.Errorf("draft %v in the window is missing", id)
		}
	}
	for _, id := range []uuid.UUID{before, after} {
		if _, ok := s.Corrections.PerDraft[id]; ok {
			t.Errorf("draft %v outside the window is counted", id)
		}
	}
	if s.Window != week {
		t.Errorf("Window = %+v, want %+v", s.Window, week)
	}
}

func TestPilotReport_GateOverSubmittedDrafts(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	llm3, final3 := corrected(3)
	gated := f.draft(draftRow{user: u, status: "submitted", created: at(1), llm: llm3, final: final3, recordID: 501})
	// Extracted and never reviewed or submitted: they would score zero and
	// dilute the ratio.
	f.draft(draftRow{user: u, status: "extracted", created: at(2), llm: `{"subject":"S"}`})
	f.draft(draftRow{user: u, status: "extracted", created: at(3), llm: `{"subject":"S"}`})
	// Reviewed but not submitted: a partial edit, not a final payload.
	llm4, final4 := corrected(4)
	f.draft(draftRow{user: u, status: "reviewed", created: at(4), llm: llm4, final: final4})

	s := f.build()
	if !reflect.DeepEqual(s.Corrections.PerDraft, map[uuid.UUID]int{gated: 3}) {
		t.Errorf("PerDraft = %v, want only the submitted draft at 3", s.Corrections.PerDraft)
	}
	if s.Corrections.DraftsOverTwo != 1 || !reflect.DeepEqual(s.Corrections.Histogram, map[int]int{3: 1}) {
		t.Errorf("Corrections = %+v, want 1 over two and histogram {3:1}", s.Corrections)
	}
	if s.Corrections.BuiltNotSubmitted != 3 || s.Drafts.Built != 4 {
		t.Errorf("BuiltNotSubmitted = %d, Built = %d, want 3 and 4", s.Corrections.BuiltNotSubmitted, s.Drafts.Built)
	}
	if len(s.Worst) != 1 || s.Worst[0].ID != gated {
		t.Errorf("Worst = %+v, want only the submitted draft", s.Worst)
	}
	if len(s.PerUser) != 1 || s.PerUser[0].Corrections != 3 {
		t.Errorf("PerUser = %+v, want corrections 3", s.PerUser)
	}
	md := report.Markdown(s)
	if !strings.Contains(md, "Drafts over two corrections: 1 over 1 submitted drafts (100%).") {
		t.Errorf("gate line wrong:\n%s", md)
	}
}

func TestPilotReport_DuplicatesScanDraftsByEventWindow(t *testing.T) {
	f := newFixture(t)
	u := f.user(42, "Pat Field")
	early := from.Add(-48 * time.Hour)
	// Created last week, submitted twice this week, with a mirror twin.
	twice := f.draft(draftRow{user: u, status: "submitted", created: early, llm: `{"subject":"Old"}`, recordID: 901})
	f.audit(twice, "attempt", at(1), `{"attempt":1,"subject":"Old"}`, "")
	f.audit(twice, "submitted", at(1).Add(time.Minute), "", `{}`)
	f.audit(twice, "submitted", at(1).Add(2*time.Minute), "", `{}`)
	f.mirrorRFI(u, 901, "Old", 42)
	f.mirrorRFI(u, 902, "Old", 42)
	// Created last week, posted this week with no outcome, now failed.
	open := f.draft(draftRow{user: u, status: "failed", created: early.Add(time.Hour), llm: `{}`, errorCode: "procore_unavailable"})
	f.audit(open, "attempt", at(2), `{"attempt":1}`, "")
	// Created and posted last week, still submitting.
	stuck := f.draft(draftRow{user: u, status: "submitting", created: early.Add(2 * time.Hour), llm: `{}`})
	f.audit(stuck, "attempt", early.Add(3*time.Hour), `{"attempt":1}`, "")
	// Last week's business: two submitted events, both before the window.
	done := f.draft(draftRow{user: u, status: "submitted", created: early.Add(4 * time.Hour), llm: `{}`, recordID: 903})
	f.audit(done, "submitted", early.Add(5*time.Hour), "", `{}`)
	f.audit(done, "submitted", early.Add(6*time.Hour), "", `{}`)

	s := f.build()
	if !reflect.DeepEqual(s.Duplicates.MultipleSubmitted, []uuid.UUID{twice}) {
		t.Errorf("MultipleSubmitted = %v, want [%v]", s.Duplicates.MultipleSubmitted, twice)
	}
	if want := []uuid.UUID{open, stuck}; !reflect.DeepEqual(s.Duplicates.UnknownAttempts, want) {
		t.Errorf("UnknownAttempts = %v, want %v", s.Duplicates.UnknownAttempts, want)
	}
	if len(s.Duplicates.MirrorMatches) != 1 || s.Duplicates.MirrorMatches[0].ProcoreID != 902 || s.Duplicates.MirrorMatches[0].DraftID != twice {
		t.Errorf("MirrorMatches = %+v, want 902 for %v", s.Duplicates.MirrorMatches, twice)
	}
	// The draft counts keep the created_at window.
	if s.Drafts.Created != 0 || s.Drafts.Submitted != 0 || s.Drafts.Failed != 0 || len(s.PerUser) != 0 {
		t.Errorf("Drafts = %+v, PerUser = %+v, want nothing created in the window", s.Drafts, s.PerUser)
	}
}
