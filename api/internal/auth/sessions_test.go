package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// testClock is a controllable time source for Sessions.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestSessions(t *testing.T) (*Sessions, *store.Store, *testClock, uuid.UUID) {
	t.Helper()
	st := storetest.New(t)
	clock := &testClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	u, err := st.Q.UpsertUserByProcoreID(context.Background(), db.UpsertUserByProcoreIDParams{
		ProcoreUserID: 42, Email: "pm@example.com", Name: "Pat Manager",
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	return NewSessions(st, []byte("test-secret-at-least-32-bytes-long!"), clock.now), st, clock, uuid.UUID(u.ID.Bytes)
}

func TestSessions_CreateAndVerify(t *testing.T) {
	s, _, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pair.ExpiresIn != 900 {
		t.Fatalf("ExpiresIn = %d, want 900", pair.ExpiresIn)
	}
	if pair.RefreshToken == "" {
		t.Fatal("empty refresh token")
	}
	claims, err := s.Verify(pair.AccessToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != userID {
		t.Fatalf("UserID = %v, want %v", claims.UserID, userID)
	}
	if claims.SessionID != sid {
		t.Fatalf("SessionID = %v, want %v", claims.SessionID, sid)
	}
}

func TestSessions_AccessTokenExpires(t *testing.T) {
	s, _, clock, userID := newTestSessions(t)

	_, pair, err := s.Create(context.Background(), userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	clock.advance(16 * time.Minute)
	if _, err := s.Verify(pair.AccessToken); err == nil {
		t.Fatal("Verify succeeded on an expired access token")
	}
}

func TestSessions_VerifyRejectsOtherAlgorithms(t *testing.T) {
	s, _, _, userID := newTestSessions(t)

	sid, _, err := s.Create(context.Background(), userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Valid claims and the right secret, but signed with HS512.
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{
		"sub": userID.String(),
		"sid": sid.String(),
		"iat": now.Unix(),
		"exp": now.Add(15 * time.Minute).Unix(),
	}).SignedString(s.secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := s.Verify(forged); err == nil {
		t.Fatal("Verify accepted an HS512 token")
	}
}

func TestSessions_RefreshRotates(t *testing.T) {
	s, _, _, userID := newTestSessions(t)
	ctx := context.Background()

	_, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	if pair2.RefreshToken == pair1.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := s.Verify(pair2.AccessToken); err != nil {
		t.Fatalf("Verify(pair2): %v", err)
	}
	if _, err := s.Refresh(ctx, pair2.RefreshToken); err != nil {
		t.Fatalf("Refresh(r2): %v", err)
	}
}

func TestSessions_RefreshReuse_RevokesFamily(t *testing.T) {
	s, st, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	if _, err := s.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("reused Refresh(r1) err = %v, want ErrSessionRevoked", err)
	}
	if _, err := s.Refresh(ctx, pair2.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh(r2) after reuse err = %v, want ErrSessionRevoked", err)
	}
	claims, err := s.Verify(pair2.AccessToken)
	if err != nil {
		t.Fatalf("Verify(pair2) should still parse: %v", err)
	}
	if claims.SessionID != sid {
		t.Fatalf("SessionID = %v, want %v", claims.SessionID, sid)
	}
	if _, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession err = %v, want pgx.ErrNoRows", err)
	}
}

func TestSessions_RefreshUnknownToken(t *testing.T) {
	s, _, _, _ := newTestSessions(t)

	if _, err := s.Refresh(context.Background(), "not-a-real-token"); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_Revoke(t *testing.T) {
	s, _, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Revoke(ctx, sid); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh after Revoke err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_Reissue(t *testing.T) {
	s, _, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Reissue(ctx, sid)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if pair2.RefreshToken == pair1.RefreshToken {
		t.Fatal("Reissue did not rotate the refresh token")
	}
	claims, err := s.Verify(pair2.AccessToken)
	if err != nil {
		t.Fatalf("Verify(reissued): %v", err)
	}
	if claims.UserID != userID || claims.SessionID != sid {
		t.Fatalf("claims = %+v, want user %v session %v", claims, userID, sid)
	}
	if _, err := s.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh(pre-reissue token) err = %v, want ErrSessionRevoked", err)
	}
	if _, err := s.Reissue(ctx, sid); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Reissue after family revocation err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_ReissueUnknownSession(t *testing.T) {
	s, _, _, _ := newTestSessions(t)

	if _, err := s.Reissue(context.Background(), uuid.New()); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_ExpiredSessionCannotRefresh(t *testing.T) {
	s, _, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	clock.advance(31 * 24 * time.Hour)
	if _, err := s.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh after expiry err = %v, want ErrSessionRevoked", err)
	}
	if _, err := s.Reissue(ctx, sid); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Reissue after expiry err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_RotateLosesRace(t *testing.T) {
	s, st, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Simulate a concurrent request that rotated between our lookup and our
	// update: the compare-and-swap must see a stale refresh hash.
	sess, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	sess.RefreshHash = []byte("stale-hash")
	if _, err := s.rotate(ctx, sess); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("rotate with stale hash err = %v, want ErrSessionRevoked", err)
	}
	if _, err := s.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh after lost race err = %v, want ErrSessionRevoked", err)
	}
}
