package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/llm/llmtest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// extraIDFinal is an extraction whose second assignee is not in the
// directory, so validation keeps [11] and flags assignee_ids.
const extraIDFinal = `{"payload":{"subject":"Twisted column","question_body":"Is it OK?","assignee_ids":[11,999],"location_id":77},"unresolved":[],"confidence":{}}`

// createFlagged creates a draft from extraIDFinal and checks the flag.
func (e *draftsEnv) createFlagged(t *testing.T) Draft {
	t.Helper()
	e.fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	e.llm.Steps = []llmtest.Step{{Final: extraIDFinal}}
	d, err := e.drafts.Create(context.Background(), e.input(uuid.New()))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(d.Unresolved) != 1 || d.Unresolved[0].Field != "assignee_ids" || d.Unresolved[0].Reason != ReasonNotInLookup {
		t.Fatalf("created unresolved = %+v, want assignee_ids not_in_lookup", d.Unresolved)
	}
	if ids := payloadMap(t, d)["assignee_ids"]; !slices.Equal(ids.([]any), []any{float64(11)}) {
		t.Fatalf("created assignee_ids = %v, want [11]", ids)
	}
	return d
}

// A not_in_lookup flag on a multi-valued field that still holds the valid
// IDs survives an unchanged submit: the reviewer has not dealt with it.
func TestSubmit_UnchangedPayload_KeepsNotInLookupFlag(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.createFlagged(t)
	q := &enqueuer{}

	_, err := e.drafts.Submit(context.Background(), e.user, d.ID, q)
	var un *ErrUnresolved
	if !errors.As(err, &un) || !slices.Equal(un.Fields, []string{"assignee_ids"}) {
		t.Fatalf("Submit = %v, want ErrUnresolved for assignee_ids", err)
	}
	if len(q.drafts) != 0 {
		t.Error("enqueued despite the flag")
	}
	got := e.mustGet(t, d.ID)
	if got.Status != StatusExtracted || len(got.Unresolved) != 1 || got.Unresolved[0].Field != "assignee_ids" {
		t.Errorf("draft = status %q unresolved %+v", got.Status, got.Unresolved)
	}
}

// Saving the same payload keeps the flag; changing the field clears it.
func TestSaveEdits_ChangedField_ClearsFlag(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.createFlagged(t)
	ctx := context.Background()

	same, err := e.drafts.SaveEdits(ctx, e.user, d.ID, d.Payload())
	if err != nil {
		t.Fatalf("unchanged SaveEdits: %v", err)
	}
	if len(same.Unresolved) != 1 || same.Unresolved[0].Field != "assignee_ids" || same.Unresolved[0].Reason != ReasonNotInLookup {
		t.Fatalf("unresolved after unchanged save = %+v, want the flag kept", same.Unresolved)
	}

	p := payloadMap(t, same)
	p["assignee_ids"] = []any{11, 42}
	edit, _ := json.Marshal(p)
	changed, err := e.drafts.SaveEdits(ctx, e.user, d.ID, edit)
	if err != nil {
		t.Fatalf("changed SaveEdits: %v", err)
	}
	if len(changed.Unresolved) != 0 {
		t.Fatalf("unresolved after changing assignee_ids = %+v, want none", changed.Unresolved)
	}
	if _, err := e.drafts.Submit(ctx, e.user, d.ID, &enqueuer{}); err != nil {
		t.Fatalf("Submit after fixing: %v", err)
	}
}

// A due date that has passed since the last save fails Submit, and the
// stored draft shows the same: due_date removed and flagged.
func TestSubmit_Unresolved_PersistsRevalidation(t *testing.T) {
	e := newDraftsEnv(t)
	e.fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	due := e.clock.Now().Add(72 * time.Hour).Format(dateLayout)
	e.llm.Steps = []llmtest.Step{{Final: `{"payload":{"subject":"Twisted column","question_body":"Is it OK?","assignee_ids":[11],"location_id":77,"due_date":"` + due + `"},"unresolved":[],"confidence":{}}`}}
	ctx := context.Background()
	d, err := e.drafts.Create(ctx, e.input(uuid.New()))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(d.Unresolved) != 0 || payloadMap(t, d)["due_date"] != due {
		t.Fatalf("created = unresolved %+v payload %s", d.Unresolved, d.Payload())
	}
	e.clock.Advance(7 * 24 * time.Hour)

	_, err = e.drafts.Submit(ctx, e.user, d.ID, &enqueuer{})
	var un *ErrUnresolved
	if !errors.As(err, &un) || !slices.Equal(un.Fields, []string{"due_date"}) {
		t.Fatalf("Submit = %v, want ErrUnresolved for due_date", err)
	}
	got := e.mustGet(t, d.ID)
	if _, ok := payloadMap(t, got)["due_date"]; ok {
		t.Errorf("stored payload still has due_date: %s", got.Payload())
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Field != "due_date" || got.Unresolved[0].Reason != ReasonDateUnparseable {
		t.Errorf("stored unresolved = %+v, want due_date", got.Unresolved)
	}
	if got.Status != StatusReviewed {
		t.Errorf("status = %q, want reviewed", got.Status)
	}
	ev := lastAudit(t, e, d.ID, AuditEdited)
	var ap struct {
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	if err := json.Unmarshal(ev.Payload, &ap); err != nil || ap.Before["due_date"] != due || ap.After["due_date"] != nil {
		t.Errorf("edited audit = %s (%v)", ev.Payload, err)
	}
}
