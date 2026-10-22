package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

func TestMigrateAndRoundTripUser(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)

	u, err := s.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{ProcoreUserID: 42, Email: "a@b.c", Name: "A"})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if u.ProcoreUserID != 42 {
		t.Errorf("ProcoreUserID = %d, want 42", u.ProcoreUserID)
	}
	if u.ReauthRequired {
		t.Errorf("ReauthRequired = true, want false")
	}

	u2, err := s.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{ProcoreUserID: 42, Email: "new@b.c", Name: "A"})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("second upsert ID = %v, want %v", u2.ID, u.ID)
	}
	if u2.Email != "new@b.c" {
		t.Errorf("Email = %q, want %q", u2.Email, "new@b.c")
	}
}

func TestConsumeAuthState_ExpiredReturnsNoRows(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)

	err := s.Q.CreateAuthState(ctx, db.CreateAuthStateParams{
		State:     "expired-state",
		DeviceID:  "device-1",
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthState: %v", err)
	}

	_, err = s.Q.ConsumeAuthState(ctx, "expired-state")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ConsumeAuthState err = %v, want pgx.ErrNoRows", err)
	}
}
