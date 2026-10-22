package auth

import (
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
// Refresh tokens are stored only as SHA-256 hashes. Presenting a refresh
// token that was already rotated away revokes the whole session.
type Sessions struct {
	st     *store.Store
	secret []byte
	now    func() time.Time
}

// NewSessions returns a Sessions signing with secret and reading time from now.
func NewSessions(st *store.Store, secret []byte, now func() time.Time) *Sessions {
	return &Sessions{st: st, secret: secret, now: now}
}

// Create starts a session for an existing user on deviceID.
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, deviceID string) (uuid.UUID, TokenPair, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return uuid.Nil, TokenPair{}, err
	}
	sess, err := s.st.Q.CreateSession(ctx, db.CreateSessionParams{
		UserID:      pgUUID(userID),
		DeviceID:    deviceID,
		RefreshHash: hash,
		ExpiresAt:   pgtype.Timestamptz{Time: s.now().Add(sessionTTL), Valid: true},
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
// token. An unknown token, an expired session, or reuse of a rotated-away
// token returns ErrSessionRevoked; reuse also deletes the session.
func (s *Sessions) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	hash := hashRefreshToken(refreshToken)
	sess, err := s.st.Q.GetSessionByRefreshHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, s.handleUnknownRefresh(ctx, hash)
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
// a fresh pair. Any refresh token issued before the call becomes a reuse.
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
// token means. If it matches a session's previous token, the token was
// reused, so the session is deleted. Either way the result is revoked.
func (s *Sessions) handleUnknownRefresh(ctx context.Context, hash []byte) error {
	sess, err := s.st.Q.GetSessionByPrevRefreshHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	if err != nil {
		return fmt.Errorf("sessions: lookup prev refresh: %w", err)
	}
	if err := s.st.Q.DeleteSession(ctx, sess.ID); err != nil {
		return fmt.Errorf("sessions: revoke reused: %w", err)
	}
	return ErrSessionRevoked
}

// rotate swaps sess's refresh hash for a new one, but only if the stored hash
// still equals sess.RefreshHash. Losing that compare-and-swap means another
// request rotated first, which is treated as reuse: the session is deleted.
func (s *Sessions) rotate(ctx context.Context, sess db.Session) (TokenPair, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	_, err = s.st.Q.RotateSessionRefresh(ctx, db.RotateSessionRefreshParams{
		ID:            sess.ID,
		RefreshHash:   hash,
		ExpiresAt:     pgtype.Timestamptz{Time: s.now().Add(sessionTTL), Valid: true},
		RefreshHash_2: sess.RefreshHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.st.Q.DeleteSession(ctx, sess.ID); err != nil {
			return TokenPair{}, fmt.Errorf("sessions: revoke raced: %w", err)
		}
		return TokenPair{}, ErrSessionRevoked
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("sessions: rotate: %w", err)
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
