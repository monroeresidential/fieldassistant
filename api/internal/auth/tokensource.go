package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// refreshSkew is how far ahead of expiry a stored access token is treated as
// stale, so a token is never handed out moments before Procore rejects it.
const refreshSkew = 60 * time.Second

// refreshTimeout bounds a refresh flight. The flight is detached from the
// caller that started it, so this is its only deadline.
const refreshTimeout = 30 * time.Second

// ErrReauthRequired means the user has no usable Procore tokens and must sign
// in again: none are stored, or Procore revoked the refresh token.
var ErrReauthRequired = errors.New("reauth required")

// TokenSource hands out a valid Procore access token per user, refreshing
// it when it is about to expire. Both tokens are stored encrypted. Concurrent
// refreshes for the same user collapse into one call to Procore.
type TokenSource struct {
	st    *store.Store
	vault *Vault
	oauth *procore.OAuth
	now   func() time.Time
	group singleflight.Group
}

// NewTokenSource returns a TokenSource reading time from now.
func NewTokenSource(st *store.Store, v *Vault, o *procore.OAuth, now func() time.Time) *TokenSource {
	return &TokenSource{st: st, vault: v, oauth: o, now: now}
}

// Store encrypts tok and saves it as userID's Procore tokens, replacing any
// previous pair.
func (t *TokenSource) Store(ctx context.Context, userID uuid.UUID, tok procore.Tokens) error {
	accessEnc, err := t.vault.Encrypt(tok.AccessToken)
	if err != nil {
		return fmt.Errorf("tokensource: encrypt access token: %w", err)
	}
	refreshEnc, err := t.vault.Encrypt(tok.RefreshToken)
	if err != nil {
		return fmt.Errorf("tokensource: encrypt refresh token: %w", err)
	}
	err = t.st.Q.UpsertProcoreTokens(ctx, db.UpsertProcoreTokensParams{
		UserID:          pgUUID(userID),
		RefreshTokenEnc: refreshEnc,
		AccessTokenEnc:  accessEnc,
		ExpiresAt:       pgtype.Timestamptz{Time: tok.ExpiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("tokensource: store tokens: %w", err)
	}
	return nil
}

// AccessToken returns a Procore access token for userID that is valid for at
// least another minute, refreshing it first if needed. It returns an error
// wrapping ErrReauthRequired when no tokens are stored or Procore revoked
// the refresh token; in the latter case the user is also marked
// reauth_required.
func (t *TokenSource) AccessToken(ctx context.Context, userID uuid.UUID) (string, error) {
	row, err := t.load(ctx, userID)
	if err != nil {
		return "", err
	}
	if t.fresh(row) {
		return t.decryptAccess(row)
	}
	v, err, _ := t.group.Do(userID.String(), func() (any, error) {
		// Procore rotates refresh tokens, so once it answers the new pair
		// must be stored even if the caller that started the flight has
		// gone away. Detaching also keeps one caller's cancellation from
		// failing every other caller waiting on the same flight.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()
		return t.refresh(fctx, userID)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// refresh runs inside the single flight for userID. It re-reads the row
// because another flight may have refreshed it since the caller looked.
func (t *TokenSource) refresh(ctx context.Context, userID uuid.UUID) (string, error) {
	row, err := t.load(ctx, userID)
	if err != nil {
		return "", err
	}
	if t.fresh(row) {
		return t.decryptAccess(row)
	}
	refreshToken, err := t.vault.Decrypt(row.RefreshTokenEnc)
	if err != nil {
		return "", fmt.Errorf("tokensource: decrypt refresh token: %w", err)
	}
	tok, err := t.oauth.Refresh(ctx, refreshToken)
	if errors.Is(err, procore.ErrTokenRevoked) {
		if err := t.st.Q.SetReauthRequired(ctx, db.SetReauthRequiredParams{
			ID: pgUUID(userID), ReauthRequired: true,
		}); err != nil {
			return "", fmt.Errorf("tokensource: mark reauth required: %w", err)
		}
		return "", fmt.Errorf("tokensource: refresh revoked: %w", ErrReauthRequired)
	}
	if err != nil {
		return "", fmt.Errorf("tokensource: refresh: %w", err)
	}
	if tok.RefreshToken == "" {
		// Procore did not rotate the refresh token; keep the current one.
		tok.RefreshToken = refreshToken
	}
	if err := t.Store(ctx, userID, tok); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// refreshToken returns userID's stored Procore refresh token, decrypted.
func (t *TokenSource) refreshToken(ctx context.Context, userID uuid.UUID) (string, error) {
	row, err := t.load(ctx, userID)
	if err != nil {
		return "", err
	}
	tok, err := t.vault.Decrypt(row.RefreshTokenEnc)
	if err != nil {
		return "", fmt.Errorf("tokensource: decrypt refresh token: %w", err)
	}
	return tok, nil
}

// load reads userID's stored tokens. A missing row means the user must sign in.
func (t *TokenSource) load(ctx context.Context, userID uuid.UUID) (db.ProcoreToken, error) {
	row, err := t.st.Q.GetProcoreTokens(ctx, pgUUID(userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ProcoreToken{}, fmt.Errorf("tokensource: no stored tokens: %w", ErrReauthRequired)
	}
	if err != nil {
		return db.ProcoreToken{}, fmt.Errorf("tokensource: load tokens: %w", err)
	}
	return row, nil
}

// fresh reports whether row's access token outlives now by more than refreshSkew.
func (t *TokenSource) fresh(row db.ProcoreToken) bool {
	return row.ExpiresAt.Time.After(t.now().Add(refreshSkew))
}

func (t *TokenSource) decryptAccess(row db.ProcoreToken) (string, error) {
	tok, err := t.vault.Decrypt(row.AccessTokenEnc)
	if err != nil {
		return "", fmt.Errorf("tokensource: decrypt access token: %w", err)
	}
	return tok, nil
}
