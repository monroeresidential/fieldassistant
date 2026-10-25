package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

func recordUser(t *testing.T, s *store.Store, procoreID int64) db.User {
	t.Helper()
	u, err := s.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{ProcoreUserID: procoreID, Email: "u@b.c", Name: "U"})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	return u
}

func upsertRecord(t *testing.T, s *store.Store, u db.User, procoreID int64, hash string) int64 {
	t.Helper()
	seq, err := s.Q.UpsertRecord(context.Background(), db.UpsertRecordParams{
		CompanyID:        1,
		UserID:           u.ID,
		ProjectID:        2,
		Kind:             "rfi",
		ProcoreID:        procoreID,
		ProcoreUpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Body:             []byte(`{"id":1}`),
		BodyHash:         hash,
	})
	if err != nil {
		t.Fatalf("UpsertRecord: %v", err)
	}
	return seq
}

func TestUpsertRecord_ChangeSeqOnlyMovesOnChange(t *testing.T) {
	s := storetest.New(t)
	u := recordUser(t, s, 7)

	s1 := upsertRecord(t, s, u, 100, "h1")
	if again := upsertRecord(t, s, u, 100, "h1"); again != s1 {
		t.Fatalf("same hash: change_seq = %d, want %d", again, s1)
	}
	if s2 := upsertRecord(t, s, u, 100, "h2"); s2 <= s1 {
		t.Fatalf("new hash: change_seq = %d, want > %d", s2, s1)
	}
}

func TestSoftDeleteMissing_ReportsOnceInDelta(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	u := recordUser(t, s, 7)

	a := upsertRecord(t, s, u, 1, "a")
	b := upsertRecord(t, s, u, 2, "b")

	sweep := func() {
		t.Helper()
		if err := s.Q.SoftDeleteMissingRecords(ctx, db.SoftDeleteMissingRecordsParams{
			UserID: u.ID, ProjectID: 2, Kind: "rfi", KeepIDs: []int64{1},
			DeletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatalf("SoftDeleteMissingRecords: %v", err)
		}
	}
	sweep()

	delta, err := s.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{UserID: u.ID, ProjectID: 2, Kind: "rfi", Since: 0, Limit: 100})
	if err != nil {
		t.Fatalf("ListRecordDelta: %v", err)
	}
	if len(delta) != 2 {
		t.Fatalf("delta len = %d, want 2", len(delta))
	}
	if delta[0].ProcoreID != 1 || delta[0].DeletedAt.Valid {
		t.Errorf("first row = %+v, want live record 1", delta[0])
	}
	got := delta[1]
	if got.ProcoreID != 2 || !got.DeletedAt.Valid {
		t.Fatalf("second row = %+v, want soft-deleted record 2", got)
	}
	if got.ChangeSeq <= a || got.ChangeSeq <= b {
		t.Errorf("deleted change_seq = %d, want > %d and %d", got.ChangeSeq, a, b)
	}

	again, err := s.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{UserID: u.ID, ProjectID: 2, Kind: "rfi", Since: got.ChangeSeq, Limit: 100})
	if err != nil {
		t.Fatalf("ListRecordDelta since: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("delta since deletion = %d rows, want 0", len(again))
	}

	// A second sweep must not report the deleted row again.
	sweep()
	again, err = s.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{UserID: u.ID, ProjectID: 2, Kind: "rfi", Since: got.ChangeSeq, Limit: 100})
	if err != nil {
		t.Fatalf("ListRecordDelta after second sweep: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("delta after second sweep = %d rows, want 0", len(again))
	}
}

func TestListRecordDelta_IsPerUser(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	u1 := recordUser(t, s, 7)
	u2 := recordUser(t, s, 8)

	upsertRecord(t, s, u1, 1, "a")
	upsertRecord(t, s, u2, 2, "b")

	for _, c := range []struct {
		u    db.User
		want int64
	}{{u1, 1}, {u2, 2}} {
		rows, err := s.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{UserID: c.u.ID, ProjectID: 2, Kind: "rfi", Since: 0, Limit: 100})
		if err != nil {
			t.Fatalf("ListRecordDelta: %v", err)
		}
		if len(rows) != 1 || rows[0].ProcoreID != c.want {
			t.Errorf("rows for user = %+v, want only record %d", rows, c.want)
		}
	}
}

func TestSyncState_RoundTripWarmTargetsAndDelete(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	u := recordUser(t, s, 7)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ts := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(d), Valid: true} }

	for _, p := range []struct {
		project int64
		req     time.Duration
	}{{2, -time.Minute}, {3, -48 * time.Hour}} {
		if err := s.Q.UpsertSyncState(ctx, db.UpsertSyncStateParams{
			CompanyID: 1, UserID: u.ID, ProjectID: p.project, Kind: "rfi",
			HighWater: ts(-time.Hour), LastPullAt: ts(-time.Hour), LastSweepAt: ts(-2 * time.Hour),
			LastRequestedAt: ts(p.req), LastError: pgtype.Text{String: "boom", Valid: true},
		}); err != nil {
			t.Fatalf("UpsertSyncState: %v", err)
		}
	}

	st, err := s.Q.GetSyncState(ctx, db.GetSyncStateParams{UserID: u.ID, ProjectID: 2, Kind: "rfi"})
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !st.HighWater.Time.Equal(now.Add(-time.Hour)) || st.LastError.String != "boom" {
		t.Errorf("sync state = %+v", st)
	}

	warm, err := s.Q.ListWarmTargets(ctx, ts(-24*time.Hour))
	if err != nil {
		t.Fatalf("ListWarmTargets: %v", err)
	}
	if len(warm) != 1 || warm[0].ProjectID != 2 {
		t.Fatalf("warm targets = %+v, want only project 2", warm)
	}

	upsertRecord(t, s, u, 1, "a")
	if err := s.Q.DeleteUserRecords(ctx, db.DeleteUserRecordsParams{UserID: u.ID, ProjectID: 2, Kind: "rfi"}); err != nil {
		t.Fatalf("DeleteUserRecords: %v", err)
	}
	rows, err := s.Q.ListRecordDelta(ctx, db.ListRecordDeltaParams{UserID: u.ID, ProjectID: 2, Kind: "rfi", Since: 0, Limit: 100})
	if err != nil {
		t.Fatalf("ListRecordDelta: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("records after delete = %d, want 0", len(rows))
	}
	if _, err := s.Q.GetSyncState(ctx, db.GetSyncStateParams{UserID: u.ID, ProjectID: 2, Kind: "rfi"}); err == nil {
		t.Errorf("sync state for project 2 survived delete")
	}
	if _, err := s.Q.GetSyncState(ctx, db.GetSyncStateParams{UserID: u.ID, ProjectID: 3, Kind: "rfi"}); err != nil {
		t.Errorf("sync state for project 3 should remain: %v", err)
	}
}
