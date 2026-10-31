package auth

import (
	"bytes"
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
	vault, err := NewVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return NewSessions(st, []byte("test-secret-at-least-32-bytes-long!"), vault, clock.now), st, clock, uuid.UUID(u.ID.Bytes)
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
	s, st, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	// Past the grace window a replay is theft, not a lost response.
	clock.advance(RefreshGrace + time.Second)
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
	s, _, clock, userID := newTestSessions(t)
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
	clock.advance(RefreshGrace + time.Second)
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

// TestSessions_RotateLosesRace_WithinGrace_ReturnsCurrentPair: a request
// that read the session before a concurrent rotation loses the
// compare-and-swap, but within the grace it gets the winner's pair.
func TestSessions_RotateLosesRace_WithinGrace_ReturnsCurrentPair(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Our request reads the session, then a concurrent request rotates first.
	sess, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	clock.advance(time.Second)
	got, err := s.rotate(ctx, sess)
	if err != nil {
		t.Fatalf("rotate after lost race within grace: %v", err)
	}
	if got.RefreshToken != pair2.RefreshToken {
		t.Fatal("lost race did not return the winner's refresh token")
	}
	claims, err := s.Verify(got.AccessToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.SessionID != sid || claims.UserID != userID {
		t.Fatalf("claims = %+v, want user %v session %v", claims, userID, sid)
	}
	if _, err := s.Refresh(ctx, pair2.RefreshToken); err != nil {
		t.Fatalf("Refresh(r2) after lost race: %v", err)
	}
}

// TestSessions_RotateLosesRace_UnrelatedHash_Revokes: losing the
// compare-and-swap to a hash that is not the session's previous one is
// treated as reuse, as before the grace window existed.
func TestSessions_RotateLosesRace_UnrelatedHash_Revokes(t *testing.T) {
	s, st, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
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

// TestSessions_RotateLosesRace_AfterGrace_Revokes: a lost race noticed after
// the grace window has closed is reuse.
func TestSessions_RotateLosesRace_AfterGrace_Revokes(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if _, err := s.Refresh(ctx, pair1.RefreshToken); err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	clock.advance(RefreshGrace + time.Second)
	if _, err := s.rotate(ctx, sess); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("rotate after grace err = %v, want ErrSessionRevoked", err)
	}
	if _, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession err = %v, want pgx.ErrNoRows", err)
	}
}

func TestSessions_RefreshReplayWithinGrace_ReturnsSamePair(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	before, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// The response carrying pair2 was lost; the phone retries with r1.
	clock.advance(3 * time.Second)
	replay, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("replayed Refresh(r1) within grace: %v", err)
	}
	if replay.RefreshToken != pair2.RefreshToken {
		t.Fatal("replay did not return pair2's refresh token")
	}
	claims, err := s.Verify(replay.AccessToken)
	if err != nil {
		t.Fatalf("Verify(replay access): %v", err)
	}
	if claims.SessionID != sid || claims.UserID != userID {
		t.Fatalf("claims = %+v, want user %v session %v", claims, userID, sid)
	}
	if err := s.Lookup(ctx, sid); err != nil {
		t.Fatalf("Lookup after replay: %v", err)
	}
	// The replay did not rotate: the stored hash is still pair2's.
	sess, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !bytes.Equal(sess.RefreshHash, hashRefreshToken(pair2.RefreshToken)) {
		t.Fatal("replay rotated the refresh token")
	}
	// Nor did it move the rotation time or the session expiry.
	if !sess.RotatedAt.Time.Equal(before.RotatedAt.Time) || !sess.ExpiresAt.Time.Equal(before.ExpiresAt.Time) {
		t.Fatalf("replay changed rotated_at/expires_at: %v/%v -> %v/%v",
			before.RotatedAt.Time, before.ExpiresAt.Time, sess.RotatedAt.Time, sess.ExpiresAt.Time)
	}
	// A second lost response: replaying again within the grace still works.
	clock.advance(3 * time.Second)
	again, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("second replay of r1 within grace: %v", err)
	}
	if again.RefreshToken != pair2.RefreshToken {
		t.Fatal("second replay did not return pair2's refresh token")
	}
	if _, err := s.Refresh(ctx, pair2.RefreshToken); err != nil {
		t.Fatalf("Refresh(r2) after replay: %v", err)
	}
}

func TestSessions_RefreshReplayAfterGrace_Revokes(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	clock.advance(121 * time.Second)
	if _, err := s.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh(r1) after grace err = %v, want ErrSessionRevoked", err)
	}
	if _, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession err = %v, want pgx.ErrNoRows", err)
	}
	if _, err := s.Refresh(ctx, pair2.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh(r2) after theft err = %v, want ErrSessionRevoked", err)
	}
}

func TestSessions_ReplayOfOlderToken_Revokes(t *testing.T) {
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
	pair3, err := s.Refresh(ctx, pair2.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r2): %v", err)
	}
	// r1 is two rotations old: no grace, even within 120 s.
	if _, err := s.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Refresh(r1) after two rotations err = %v, want ErrSessionRevoked", err)
	}
	// Only one previous hash is kept, so r1 cannot be tied to the session:
	// the session survives, as before the grace window existed.
	if _, err := s.Refresh(ctx, pair3.RefreshToken); err != nil {
		t.Fatalf("Refresh(r3) after stale r1: %v", err)
	}
}

func TestSessions_CreateStoresEncryptedRefresh(t *testing.T) {
	s, st, _, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if bytes.Contains(sess.RefreshEnc, []byte(pair1.RefreshToken)) {
		t.Fatal("refresh_enc holds the plaintext token")
	}
	got, err := s.vault.Decrypt(sess.RefreshEnc)
	if err != nil {
		t.Fatalf("Decrypt(refresh_enc): %v", err)
	}
	if got != pair1.RefreshToken {
		t.Fatal("refresh_enc does not decrypt to the issued refresh token")
	}

	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	sess, err = st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	got, err = s.vault.Decrypt(sess.RefreshEnc)
	if err != nil {
		t.Fatalf("Decrypt(refresh_enc) after rotate: %v", err)
	}
	if got != pair2.RefreshToken {
		t.Fatal("rotate did not store the new refresh token in refresh_enc")
	}
}

// rotatedOnce creates a session and rotates it once, returning the session
// id, the rotated-away pair and the current pair.
func rotatedOnce(t *testing.T, s *Sessions, userID uuid.UUID) (uuid.UUID, TokenPair, TokenPair) {
	t.Helper()
	ctx := context.Background()
	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pair2, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh(r1): %v", err)
	}
	return sid, pair1, pair2
}

// wantReplayRevokes asserts that replaying r1 is theft: ErrSessionRevoked
// and the session deleted.
func wantReplayRevokes(t *testing.T, s *Sessions, st *store.Store, sid uuid.UUID, r1 string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Refresh(ctx, r1); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("replay err = %v, want ErrSessionRevoked", err)
	}
	if _, err := st.Q.GetSession(ctx, pgtype.UUID{Bytes: sid, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSession err = %v, want pgx.ErrNoRows", err)
	}
}

// TestSessions_ReplayWithoutGraceColumns_Revokes: a row rotated before the
// grace window existed has no rotated_at or no refresh_enc; a replay of its
// previous token is theft, as before.
func TestSessions_ReplayWithoutGraceColumns_Revokes(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
	}{
		{"null rotated_at", "update sessions set rotated_at = null where id = $1"},
		{"null refresh_enc", "update sessions set refresh_enc = null where id = $1"},
		{"empty refresh_enc", "update sessions set refresh_enc = ''::bytea where id = $1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, clock, userID := newTestSessions(t)
			sid, pair1, _ := rotatedOnce(t, s, userID)
			if _, err := st.Pool.Exec(context.Background(), tc.sql, sid); err != nil {
				t.Fatalf("simulate pre-migration row: %v", err)
			}
			clock.advance(3 * time.Second)
			wantReplayRevokes(t, s, st, sid, pair1.RefreshToken)
		})
	}
}

// TestSessions_ReplayWithFutureRotation_Revokes: if the clock steps back so
// rotated_at lies in the future, the elapsed time is negative and the replay
// is not in grace.
func TestSessions_ReplayWithFutureRotation_Revokes(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	sid, pair1, _ := rotatedOnce(t, s, userID)
	clock.advance(-10 * time.Minute)
	wantReplayRevokes(t, s, st, sid, pair1.RefreshToken)
}

// TestSessions_ReplayOnExpiredSession_Revokes: within the grace but past the
// session's expiry, a replay is not answered.
func TestSessions_ReplayOnExpiredSession_Revokes(t *testing.T) {
	s, st, clock, userID := newTestSessions(t)
	sid, pair1, _ := rotatedOnce(t, s, userID)
	if _, err := st.Pool.Exec(context.Background(),
		"update sessions set expires_at = $2 where id = $1", sid, clock.now().Add(time.Second)); err != nil {
		t.Fatalf("shorten expiry: %v", err)
	}
	clock.advance(3 * time.Second)
	wantReplayRevokes(t, s, st, sid, pair1.RefreshToken)
}

// TestSessions_ReissueThenReplayWithinGrace_ReturnsReissuedPair: Reissue
// rotates like Refresh, so the pre-reissue token replayed within the grace
// gets the reissued pair.
func TestSessions_ReissueThenReplayWithinGrace_ReturnsReissuedPair(t *testing.T) {
	s, _, clock, userID := newTestSessions(t)
	ctx := context.Background()

	sid, pair1, err := s.Create(ctx, userID, "device-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reissued, err := s.Reissue(ctx, sid)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	clock.advance(3 * time.Second)
	replay, err := s.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("replay of pre-reissue token within grace: %v", err)
	}
	if replay.RefreshToken != reissued.RefreshToken {
		t.Fatal("replay did not return the reissued refresh token")
	}
}
