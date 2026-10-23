package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
)

// appRedirectBase is where the callback sends the browser back to the app.
const appRedirectBase = "fieldassistant://auth"

// Length bounds for code_challenge and code_verifier, as in RFC 7636.
const (
	minPKCELen = 43
	maxPKCELen = 128
)

// validChallenge reports whether c is 43-128 characters of [A-Za-z0-9_-].
func validChallenge(c string) bool {
	if len(c) < minPKCELen || len(c) > maxPKCELen {
		return false
	}
	for _, r := range c {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func badRequest(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: msg}
}

func (s *Server) AuthStart(ctx context.Context, req AuthStartRequestObject) (AuthStartResponseObject, error) {
	if req.Body.DeviceId == "" {
		return nil, badRequest("device_id is required")
	}
	if !validChallenge(req.Body.CodeChallenge) {
		return nil, badRequest("code_challenge must be 43-128 base64url characters")
	}
	authorizeURL, state, err := s.deps.Broker.Start(ctx, req.Body.DeviceId, req.Body.CodeChallenge)
	if err != nil {
		return nil, err
	}
	return AuthStart200JSONResponse{AuthorizeUrl: authorizeURL, State: state}, nil
}

// Callback error codes sent back to the app in the redirect's error parameter.
const (
	callbackAccessDenied       = "access_denied"
	callbackBadRequest         = "bad_request"
	callbackProcoreUnavailable = "procore_unavailable"
	callbackInternal           = "internal"
)

// AuthCallback always redirects to the app, because it runs in the sign-in
// browser sheet where an error body would be a dead end. Failures carry an
// error code instead of a one-time code.
func (s *Server) AuthCallback(ctx context.Context, req AuthCallbackRequestObject) (AuthCallbackResponseObject, error) {
	log := s.logger()
	if req.Params.Error != nil {
		// Any Procore OAuth error ends this sign-in attempt; spend the state.
		if err := s.deps.Broker.Abandon(ctx, req.Params.State); err != nil {
			log.WarnContext(ctx, "auth callback: abandon state", "error", err)
		}
		log.WarnContext(ctx, "auth callback: procore reported an error", "procore_error", *req.Params.Error)
		return callbackRedirect("error", callbackAccessDenied), nil
	}
	if req.Params.Code == nil || *req.Params.Code == "" {
		log.WarnContext(ctx, "auth callback: missing code")
		return callbackRedirect("error", callbackBadRequest), nil
	}
	code, err := s.deps.Broker.Callback(ctx, *req.Params.Code, req.Params.State)
	if err != nil {
		reason := callbackInternal
		switch {
		case errors.Is(err, auth.ErrBadState):
			reason = callbackBadRequest
		case errors.Is(err, auth.ErrProcoreUnavailable):
			reason = callbackProcoreUnavailable
		}
		log.WarnContext(ctx, "auth callback failed", "reason", reason, "error", err)
		return callbackRedirect("error", reason), nil
	}
	return callbackRedirect("code", code), nil
}

// callbackRedirect sends the browser to the app with one query parameter.
func callbackRedirect(key, value string) AuthCallback302Response {
	loc := appRedirectBase + "?" + url.Values{key: {value}}.Encode()
	return AuthCallback302Response{Headers: AuthCallback302ResponseHeaders{Location: loc}}
}

func (s *Server) AuthExchange(ctx context.Context, req AuthExchangeRequestObject) (AuthExchangeResponseObject, error) {
	if n := len(req.Body.CodeVerifier); n < minPKCELen || n > maxPKCELen {
		return nil, badRequest("code_verifier must be 43-128 characters")
	}
	pair, err := s.deps.Broker.Exchange(ctx, req.Body.Code, req.Body.DeviceId, req.Body.CodeVerifier)
	if err != nil {
		return nil, authError(err)
	}
	return AuthExchange200JSONResponse(tokenPair(pair)), nil
}

func (s *Server) AuthRefresh(ctx context.Context, req AuthRefreshRequestObject) (AuthRefreshResponseObject, error) {
	pair, err := s.deps.Sessions.Refresh(ctx, req.Body.RefreshToken)
	if err != nil {
		return nil, authError(err)
	}
	return AuthRefresh200JSONResponse(tokenPair(pair)), nil
}

func (s *Server) AuthLogout(ctx context.Context, _ AuthLogoutRequestObject) (AuthLogoutResponseObject, error) {
	if err := s.deps.Broker.Logout(ctx, claimsFrom(ctx).SessionID); err != nil {
		return nil, err
	}
	return AuthLogout204Response{}, nil
}

// GetMe returns the signed-in user's record.
func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	u, err := s.deps.Store.Q.GetUser(ctx, pgtype.UUID{Bytes: claimsFrom(ctx).UserID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "user not found"}
	}
	if err != nil {
		return nil, err
	}
	return GetMe200JSONResponse{
		Id:             u.ID.Bytes,
		ProcoreUserId:  u.ProcoreUserID,
		Email:          u.Email,
		Name:           u.Name,
		ReauthRequired: u.ReauthRequired,
		LastCompanyId:  int8Ptr(u.LastCompanyID),
		LastProjectId:  int8Ptr(u.LastProjectID),
	}, nil
}

// authError maps auth flow errors to API errors; others pass through to the
// strict error handler as 500s.
func authError(err error) error {
	switch {
	case errors.Is(err, auth.ErrBadState):
		return &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "invalid or expired authorization"}
	case errors.Is(err, auth.ErrSessionRevoked):
		return &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "session revoked"}
	case errors.Is(err, auth.ErrProcoreUnavailable):
		return &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "Procore sign-in failed"}
	}
	return err
}

func tokenPair(p auth.TokenPair) TokenPair {
	return TokenPair{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, ExpiresIn: p.ExpiresIn}
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
