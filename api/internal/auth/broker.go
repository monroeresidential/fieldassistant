package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

const (
	stateTTL = 10 * time.Minute
	codeTTL  = 60 * time.Second
)

var (
	// ErrBadState means an OAuth state or one-time code is unknown, expired,
	// already used, or was presented from a different device or with a code
	// verifier that does not match its challenge.
	ErrBadState = errors.New("bad auth state")
	// ErrProcoreUnavailable wraps a Procore failure during sign-in.
	ErrProcoreUnavailable = errors.New("procore unavailable")
)

// Broker runs the sign-in flow. The app starts it with a device id, the
// browser returns through Procore to Callback, and the app trades the
// resulting one-time code for session tokens with Exchange. Procore tokens
// never leave the server.
type Broker struct {
	st  *store.Store
	o   *procore.OAuth
	pc  *procore.Client
	ts  *TokenSource
	ss  *Sessions
	now func() time.Time
}

// NewBroker returns a Broker reading time from now.
func NewBroker(st *store.Store, o *procore.OAuth, pc *procore.Client, ts *TokenSource, ss *Sessions, now func() time.Time) *Broker {
	return &Broker{st: st, o: o, pc: pc, ts: ts, ss: ss, now: now}
}

// Start records a fresh OAuth state for deviceID and codeChallenge and
// returns the Procore consent URL carrying it. codeChallenge is
// base64url-nopad(SHA-256(verifier)) for a verifier only the app knows;
// Exchange demands that verifier.
func (b *Broker) Start(ctx context.Context, deviceID, codeChallenge string) (authorizeURL, state string, err error) {
	state, err = randomToken()
	if err != nil {
		return "", "", err
	}
	err = b.st.Q.CreateAuthState(ctx, db.CreateAuthStateParams{
		State:         state,
		DeviceID:      deviceID,
		CodeChallenge: codeChallenge,
		ExpiresAt:     pgtype.Timestamptz{Time: b.now().Add(stateTTL), Valid: true},
	})
	if err != nil {
		return "", "", fmt.Errorf("broker: store state: %w", err)
	}
	return b.o.AuthorizeURL(state), state, nil
}

// Callback completes Procore consent: it consumes state, trades code for
// Procore tokens, records the user and their tokens, starts a session on the
// state's device and returns a one-time code for that session, bound to the
// state's code challenge. An unknown or
// expired state returns ErrBadState; a Procore failure wraps
// ErrProcoreUnavailable.
func (b *Broker) Callback(ctx context.Context, code, state string) (string, error) {
	authState, err := b.st.Q.ConsumeAuthState(ctx, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrBadState
	}
	if err != nil {
		return "", fmt.Errorf("broker: consume state: %w", err)
	}

	tok, err := b.o.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("broker: exchange code: %w: %w", ErrProcoreUnavailable, err)
	}
	me, err := b.pc.Me(ctx, tok.AccessToken)
	if err != nil {
		return "", fmt.Errorf("broker: fetch me: %w: %w", ErrProcoreUnavailable, err)
	}

	user, err := b.st.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{
		ProcoreUserID: me.ID, Email: me.Login, Name: me.Name,
	})
	if err != nil {
		return "", fmt.Errorf("broker: upsert user: %w", err)
	}
	userID := uuid.UUID(user.ID.Bytes)
	if err := b.ts.Store(ctx, userID, tok); err != nil {
		return "", err
	}
	// Fresh tokens are stored, so a previous revocation no longer applies.
	if err := b.st.Q.SetReauthRequired(ctx, db.SetReauthRequiredParams{ID: user.ID, ReauthRequired: false}); err != nil {
		return "", fmt.Errorf("broker: clear reauth required: %w", err)
	}
	// The pair is discarded: Exchange reissues one for the session, so no
	// usable token passes through the browser.
	sessionID, _, err := b.ss.Create(ctx, userID, authState.DeviceID)
	if err != nil {
		return "", err
	}

	oneTimeCode, err := randomToken()
	if err != nil {
		return "", err
	}
	err = b.st.Q.CreateAuthCode(ctx, db.CreateAuthCodeParams{
		Code:          oneTimeCode,
		SessionID:     pgUUID(sessionID),
		CodeChallenge: authState.CodeChallenge,
		ExpiresAt:     pgtype.Timestamptz{Time: b.now().Add(codeTTL), Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("broker: store code: %w", err)
	}
	return oneTimeCode, nil
}

// Exchange consumes oneTimeCode and returns a fresh pair for its session.
// An unknown, expired or used code, a codeVerifier that does not hash to the
// challenge given to Start, or a deviceID other than the one that started
// sign-in returns ErrBadState. A code is spent even when a check fails.
func (b *Broker) Exchange(ctx context.Context, oneTimeCode, deviceID, codeVerifier string) (TokenPair, error) {
	row, err := b.st.Q.ConsumeAuthCode(ctx, oneTimeCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrBadState
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("broker: consume code: %w", err)
	}
	if !verifierMatches(codeVerifier, row.CodeChallenge) {
		return TokenPair{}, ErrBadState
	}
	sess, err := b.st.Q.GetSession(ctx, row.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenPair{}, ErrBadState
	}
	if err != nil {
		return TokenPair{}, fmt.Errorf("broker: load session: %w", err)
	}
	if sess.DeviceID != deviceID {
		return TokenPair{}, ErrBadState
	}
	return b.ss.Reissue(ctx, uuid.UUID(row.SessionID.Bytes))
}

// Logout revokes the Procore refresh token of the session's user, ignoring
// Procore errors, then revokes the session.
func (b *Broker) Logout(ctx context.Context, sessionID uuid.UUID) error {
	sess, err := b.st.Q.GetSession(ctx, pgUUID(sessionID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Already gone; revoking below is a no-op.
	case err != nil:
		return fmt.Errorf("broker: load session: %w", err)
	default:
		b.revokeProcore(ctx, uuid.UUID(sess.UserID.Bytes))
	}
	return b.ss.Revoke(ctx, sessionID)
}

// revokeProcore best-effort revokes userID's stored Procore refresh token.
// Logout succeeds regardless, so every failure is ignored.
func (b *Broker) revokeProcore(ctx context.Context, userID uuid.UUID) {
	refresh, err := b.ts.refreshToken(ctx, userID)
	if err != nil {
		return
	}
	_ = b.o.Revoke(ctx, refresh)
}

// verifierMatches reports whether base64url-nopad(SHA-256(verifier)) equals
// challenge, comparing in constant time.
func verifierMatches(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// randomToken returns 32 random bytes as base64url.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("broker: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
