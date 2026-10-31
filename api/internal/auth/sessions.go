package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

const (
	accessTokenTTL = 15 * time.Minute
	sessionTTL     = 30 * 24 * time.Hour
)

// RefreshGrace is how long after a rotation the rotated-away refresh token
// may be presented again and receive the current pair instead of revoking
// the session. It covers a refresh response lost on a flaky connection.
const RefreshGrace = 120 * time.Second

// ErrSessionRevoked is returned when a refresh token is unknown, reused, or
// belongs to a session that was revoked or has expired.
var ErrSessionRevoked = errors.New("session revoked")

// TokenPair is what a client receives after sign-in or refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // access token lifetime in seconds
}

// Claims are the identities carried by a verified access token.
type Claims struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
}

// accessClaims is the JWT payload: sub (user id), sid (session id), iat, exp.
type accessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Sessions issues HS256 access JWTs and rotating, single-use refresh tokens.
// Refresh tokens are looked up by SHA-256 hash; the current one is also kept
// vault-encrypted so a replay within RefreshGrace can be answered. Presenting
// the token rotated away more than RefreshGrace ago revokes the whole session.
type Sessions struct {
	st     *store.Store
	secret []byte
	vault  *Vault
	now    func() time.Time
}

// NewSessions returns a Sessions signing with secret, encrypting the current
// refresh token with vault and reading time from now.
func NewSessions(st *store.Store, secret []byte, vault *Vault, now func() time.Time) *Sessions {
	return &Sessions{st: st, secret: secret, vault: vault, now: now}
}

// Create starts a session for an existing user on deviceID.
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, deviceID string) (uuid.UUID, TokenPair, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return uuid.Nil, TokenPair{}, err
	}
	enc, err := s.vault.Encrypt(refresh)
	if err != nil {
		return uuid.Nil, TokenPair{}, fmt.Errorf("sessions: encrypt refresh: %w", err)
	}
	sess, err := s.st.Q.CreateSession(ctx, db.CreateSessionParams{
		UserID:      pgUUID(userID),
		DeviceID:    deviceID,
		RefreshHash: hash,
		ExpiresAt:   pgtype.Timestamptz{Time: s.now().Add(sessionTTL), Valid: true},
		RefreshEnc:  enc,
	})
	if err != nil {
		return uuid.Nil, TokenPair{}, fmt.Errorf("sessions: create: %w", err)
	}
	sessionID := uuid.UUID(sess.ID.Bytes)
	pair, err := s.pair(userID, sessionID, refresh)
	if err != nil {
		return uuid.Nil, TokenPair{}, err
	}
	return sessionID, pair, nil
}

// Refresh exchanges a refresh token for a new pair, rotating the refresh
// token. Presenting the token rotated away at most RefreshGrace ago returns
// the current pair without rotating. An unknown token, an expired session,
// or reuse of a rotated-away token after the grace returns
// ErrSessionRevoked; that reuse also deletes the session.
func (s *Sessions) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	hash := hashRefreshToken(refreshToken)
	sess, err := s.st.Q.GetSessionByRefreshHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.handleUnknownRefresh(ctx, hash)
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: lookup refresh: %w", err)
	}
	if s.expired(sess) {
		return TokenPair{}, ErrSessionRevoked
	}
	return s.rotate(ctx, sess)
}

// Reissue rotates an existing, unexpired session's refresh token and returns
// a fresh pair. Any refresh token issued before the call becomes a reuse,
// subject to the same RefreshGrace as Refresh.
func (s *Sessions) Reissue(ctx context.Context, sessionID uuid.UUID) (TokenPair, error) {
	sess, err := s.st.Q.GetSession(ctx, pgUUID(sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrSessionRevoked
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: lookup session: %w", err)
	}
	if s.expired(sess) {
		return TokenPair{}, ErrSessionRevoked
	}
	return s.rotate(ctx, sess)
}

// Verify checks an access token's HS256 signature and expiry. It does not
// consult the database; callers must confirm the session still exists.
func (s *Sessions) Verify(accessToken string) (Claims, error) {
	var c accessClaims
	_, err := jwt.ParseWithClaims(accessToken, &c,
		func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(s.now),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("sessions: verify: %w", err)
	}
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return Claims{}, fmt.Errorf("sessions: verify: bad sub: %w", err)
	}
	sessionID, err := uuid.Parse(c.SessionID)
	if err != nil {
		return Claims{}, fmt.Errorf("sessions: verify: bad sid: %w", err)
	}
	return Claims{UserID: userID, SessionID: sessionID}, nil
}

// Lookup confirms sessionID still exists and is unexpired, returning
// ErrSessionRevoked otherwise. Pair it with Verify to authenticate a request.
func (s *Sessions) Lookup(ctx context.Context, sessionID uuid.UUID) error {
	sess, err := s.st.Q.GetSession(ctx, pgUUID(sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	if err != nil {
		return fmt.Errorf("sessions: lookup session: %w", err)
	}
	if s.expired(sess) {
		return ErrSessionRevoked
	}
	return nil
}

// Revoke deletes a session. Revoking a missing session is not an error.
func (s *Sessions) Revoke(ctx context.Context, sessionID uuid.UUID) error {
	if err := s.st.Q.DeleteSession(ctx, pgUUID(sessionID)); err != nil {
		return fmt.Errorf("sessions: revoke: %w", err)
	}
	return nil
}

// handleUnknownRefresh decides what a refresh hash that matches no current
// token means. If it matches a session's previous token rotated away within
// RefreshGrace, the response carrying the current pair was probably lost, so
// that pair is returned again. Otherwise a match is reuse and the session is
// deleted. Every other outcome is ErrSessionRevoked.
func (s *Sessions) handleUnknownRefresh(ctx context.Context, hash []byte) (TokenPair, error) {
	sess, err := s.st.Q.GetSessionByPrevRefreshHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrSessionRevoked
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: lookup prev refresh: %w", err)
	}
	if s.inGrace(sess) {
		return s.currentPair(sess)
	}
	if err := s.st.Q.DeleteSession(ctx, sess.ID); err != nil {
		return TokenPair{}, fmt.Errorf("sessions: revoke reused: %w", err)
	}
	return TokenPair{}, ErrSessionRevoked
}

// rotate swaps sess's refresh hash for a new one, but only if the stored hash
// still equals sess.RefreshHash. Losing that compare-and-swap means another
// request rotated first. If that rotation replaced exactly sess.RefreshHash
// within RefreshGrace, its pair is returned; otherwise it is treated as
// reuse and the session is deleted.
func (s *Sessions) rotate(ctx context.Context, sess db.Session) (TokenPair, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	enc, err := s.vault.Encrypt(refresh)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: encrypt refresh: %w", err)
	}
	now := s.now()
	_, err = s.st.Q.RotateSessionRefresh(ctx, db.RotateSessionRefreshParams{
		ID:            sess.ID,
		RefreshHash:   hash,
		ExpiresAt:     pgtype.Timestamptz{Time: now.Add(sessionTTL), Valid: true},
		RefreshHash_2: sess.RefreshHash,
		RefreshEnc:    enc,
		RotatedAt:     pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.lostRace(ctx, sess)
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: rotate: %w", err)
	}
	return s.pair(uuid.UUID(sess.UserID.Bytes), uuid.UUID(sess.ID.Bytes), refresh)
}

// lostRace handles a rotate whose compare-and-swap on stale.RefreshHash
// failed. It re-reads the session: if the winning rotation replaced exactly
// stale.RefreshHash within RefreshGrace, the current pair is returned;
// otherwise the session is deleted and ErrSessionRevoked returned.
func (s *Sessions) lostRace(ctx context.Context, stale db.Session) (TokenPair, error) {
	cur, err := s.st.Q.GetSession(ctx, stale.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrSessionRevoked
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: reread raced: %w", err)
	}
	if bytes.Equal(cur.PrevRefreshHash, stale.RefreshHash) && s.inGrace(cur) {
		return s.currentPair(cur)
	}
	if err := s.st.Q.DeleteSession(ctx, stale.ID); err != nil {
		return TokenPair{}, fmt.Errorf("sessions: revoke raced: %w", err)
	}
	return TokenPair{}, ErrSessionRevoked
}

// inGrace reports whether sess was rotated at most RefreshGrace ago by the
// injected clock and is unexpired, so its previous refresh token may be
// answered with the current pair.
func (s *Sessions) inGrace(sess db.Session) bool {
	if !sess.RotatedAt.Valid || len(sess.RefreshEnc) == 0 || s.expired(sess) {
		return false
	}
	return s.now().Sub(sess.RotatedAt.Time) <= RefreshGrace
}

// currentPair returns sess's current refresh token, decrypted from
// refresh_enc, with a freshly signed access token. It does not rotate.
func (s *Sessions) currentPair(sess db.Session) (TokenPair, error) {
	refresh, err := s.vault.Decrypt(sess.RefreshEnc)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: decrypt refresh: %w", err)
	}
	return s.pair(uuid.UUID(sess.UserID.Bytes), uuid.UUID(sess.ID.Bytes), refresh)
}

// expired reports whether sess has reached expires_at by the injected clock.
func (s *Sessions) expired(sess db.Session) bool {
	return !s.now().Before(sess.ExpiresAt.Time)
}

// pair signs a fresh access token and bundles it with refresh.
func (s *Sessions) pair(userID, sessionID uuid.UUID, refresh string) (TokenPair, error) {
	now := s.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		SessionID: sessionID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
	})
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: sign access token: %w", err)
	}
	return TokenPair{
		AccessToken:  signed,
		RefreshToken: refresh,
		ExpiresIn:    int(accessTokenTTL / time.Second),
	}, nil
}

// newRefreshToken returns 32 random bytes as base64url and their SHA-256 hash.
func newRefreshToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("sessions: random refresh token: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, hashRefreshToken(tok), nil
}

func hashRefreshToken(tok string) []byte {
	sum := sha256.Sum256([]byte(tok))
	return sum[:]
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
