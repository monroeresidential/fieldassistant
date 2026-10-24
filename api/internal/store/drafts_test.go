package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

func TestDrafts_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)

	u, err := s.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{ProcoreUserID: 7, Email: "u@b.c", Name: "U"})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	other, err := s.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{ProcoreUserID: 8, Email: "o@b.c", Name: "O"})
	if err != nil {
		t.Fatalf("upsert other user: %v", err)
	}

	id := uuid.New()
	pid := pgtype.UUID{Bytes: id, Valid: true}
	if _, err := s.Q.InsertDraft(ctx, db.InsertDraftParams{
		ID:            pid,
		CompanyID:     1,
		ProjectID:     2,
		UserID:        u.ID,
		Feature:       "rfi",
		Status:        "extracted",
		Transcript:    "hello",
		CapturedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		LlmOutput:     []byte(`{"subject":"x"}`),
		Unresolved:    []byte(`[]`),
		Confidence:    []byte(`{}`),
		ModelVersion:  pgtype.Text{String: "m", Valid: true},
		PromptVersion: pgtype.Text{String: "p1", Valid: true},
		ExtractMs:     pgtype.Int4{Int32: 120, Valid: true},
	}); err != nil {
		t.Fatalf("InsertDraft: %v", err)
	}

	if err := s.Q.SetDraftUserPayload(ctx, db.SetDraftUserPayloadParams{ID: pid, UserPayload: []byte(`{"subject":"y"}`)}); err != nil {
		t.Fatalf("SetDraftUserPayload: %v", err)
	}
	if err := s.Q.SetDraftSubmitted(ctx, db.SetDraftSubmittedParams{
		ID:                  pid,
		ProcoreRecordID:     pgtype.Int8{Int64: 501, Valid: true},
		ProcoreRecordNumber: pgtype.Text{String: "214", Valid: true},
		ProcoreUrl:          pgtype.Text{String: "https://example/rfi/501", Valid: true},
	}); err != nil {
		t.Fatalf("SetDraftSubmitted: %v", err)
	}

	d, err := s.Q.GetDraftForUser(ctx, db.GetDraftForUserParams{ID: pid, UserID: u.ID})
	if err != nil {
		t.Fatalf("GetDraftForUser: %v", err)
	}
	if d.Status != "submitted" {
		t.Errorf("Status = %q, want submitted", d.Status)
	}
	if d.ProcoreRecordNumber.String != "214" {
		t.Errorf("ProcoreRecordNumber = %q, want 214", d.ProcoreRecordNumber.String)
	}
	if string(d.UserPayload) != `{"subject": "y"}` {
		t.Errorf("UserPayload = %s", d.UserPayload)
	}

	n, err := s.Q.CountSubmittedDrafts(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountSubmittedDrafts: %v", err)
	}
	if n != 1 {
		t.Errorf("CountSubmittedDrafts = %d, want 1", n)
	}

	if _, err := s.Q.GetDraftForUser(ctx, db.GetDraftForUserParams{ID: pid, UserID: other.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("other user err = %v, want pgx.ErrNoRows", err)
	}

	list, err := s.Q.ListDraftsForUserProject(ctx, db.ListDraftsForUserProjectParams{UserID: u.ID, ProjectID: 2, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListDraftsForUserProject = %d rows, err %v", len(list), err)
	}

	if err := s.Q.SetDraftFailed(ctx, db.SetDraftFailedParams{
		ID:           pid,
		ErrorCode:    pgtype.Text{String: "procore_validation", Valid: true},
		ErrorMessage: pgtype.Text{String: "bad", Valid: true},
		Unresolved:   []byte(`[{"field":"subject"}]`),
	}); err != nil {
		t.Fatalf("SetDraftFailed: %v", err)
	}
	if err := s.Q.SetDraftJob(ctx, db.SetDraftJobParams{ID: pid, JobID: pgtype.Int8{Int64: 9, Valid: true}}); err != nil {
		t.Fatalf("SetDraftJob: %v", err)
	}
	d, err = s.Q.GetDraft(ctx, pid)
	if err != nil {
		t.Fatalf("GetDraft: %v", err)
	}
	if d.Status != "failed" || d.ErrorCode.String != "procore_validation" || d.JobID.Int64 != 9 {
		t.Errorf("draft after failure/job = %+v", d)
	}
	if err := s.Q.SetDraftStatus(ctx, db.SetDraftStatusParams{ID: pid, Status: "extracted"}); err != nil {
		t.Fatalf("SetDraftStatus: %v", err)
	}
	d, err = s.Q.GetDraft(ctx, pid)
	if err != nil || d.Status != "extracted" {
		t.Fatalf("after SetDraftStatus: %+v, err %v", d, err)
	}

	if _, err := s.Q.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		CompanyID: 1, DraftID: pid, Kind: "submit",
		Payload: []byte(`{}`), DurationMs: pgtype.Int4{Int32: 5, Valid: true},
	}); err != nil {
		t.Fatalf("InsertAuditEvent: %v", err)
	}
	evs, err := s.Q.ListAuditEvents(ctx, pid)
	if err != nil || len(evs) != 1 || evs[0].Kind != "submit" {
		t.Fatalf("ListAuditEvents = %v, err %v", evs, err)
	}
}

func TestLookups_Upsert(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)

	p := db.UpsertLookupParams{CompanyID: 1, ProjectID: 2, Kind: "people", Etag: "e1", Body: []byte(`[1]`), FetchedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	if err := s.Q.UpsertLookup(ctx, p); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	p.Etag, p.Body = "e2", []byte(`[2]`)
	if err := s.Q.UpsertLookup(ctx, p); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := s.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: 1, ProjectID: 2, Kind: "people"})
	if err != nil {
		t.Fatalf("GetLookup: %v", err)
	}
	if got.Etag != "e2" || string(got.Body) != "[2]" {
		t.Errorf("lookup = etag %q body %s, want e2 [2]", got.Etag, got.Body)
	}
}
