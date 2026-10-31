// Package procore talks to Procore's OAuth and REST APIs.
package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/config"
)

// ErrTokenRevoked means Procore rejected the refresh token as invalid or revoked.
var ErrTokenRevoked = errors.New("procore token revoked")

// maxErrBody caps how much of an upstream error body is echoed into errors:
// an APIError's Body is at most this many runes.
const maxErrBody = 200

// maxErrorBodyParse caps how much of an API error body is read to parse its
// field errors. A real 422 runs well past maxErrBody.
const maxErrorBodyParse = 64 << 10

// Tokens is a Procore access/refresh token pair.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// OAuth is a client for Procore's OAuth endpoints.
type OAuth struct {
	cfg config.ProcoreConfig
	hc  *http.Client
	now func() time.Time
}

// NewOAuth builds an OAuth client. A nil hc uses http.DefaultClient.
func NewOAuth(cfg config.ProcoreConfig, hc *http.Client, now func() time.Time) *OAuth {
	if hc == nil {
		hc = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &OAuth{cfg: cfg, hc: hc, now: now}
}

// AuthorizeURL returns the Procore consent URL for the given state.
func (o *OAuth) AuthorizeURL(state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {o.cfg.ClientID},
		"redirect_uri":  {o.cfg.RedirectURL},
		"state":         {state},
	}
	return strings.TrimRight(o.cfg.LoginURL, "/") + "/oauth/authorize?" + q.Encode()
}

// Exchange trades an authorization code for tokens.
func (o *OAuth) Exchange(ctx context.Context, code string) (Tokens, error) {
	return o.token(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {o.cfg.RedirectURL},
	})
}

// Refresh trades a refresh token for new tokens. It returns ErrTokenRevoked
// when Procore answers 400 or 401.
func (o *OAuth) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	t, err := o.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
	var se *statusError
	if errors.As(err, &se) && (se.status == http.StatusBadRequest || se.status == http.StatusUnauthorized) {
		return Tokens{}, fmt.Errorf("refresh rejected with status %d: %w", se.status, ErrTokenRevoked)
	}
	return t, err
}

// Revoke revokes an access or refresh token.
func (o *OAuth) Revoke(ctx context.Context, token string) error {
	resp, err := o.post(ctx, "/oauth/revoke", url.Values{"token": {token}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newStatusError("revoke", resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (o *OAuth) token(ctx context.Context, form url.Values) (Tokens, error) {
	resp, err := o.post(ctx, "/oauth/token", form)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Tokens{}, newStatusError("token request", resp)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		CreatedAt    int64  `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Tokens{}, fmt.Errorf("procore: decode token response: %w", err)
	}
	if body.AccessToken == "" {
		return Tokens{}, errors.New("procore: token response missing access_token")
	}
	created := o.now()
	if body.CreatedAt != 0 {
		created = time.Unix(body.CreatedAt, 0)
	}
	return Tokens{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    created.Add(time.Duration(body.ExpiresIn) * time.Second),
	}, nil
}

// post sends a form POST to the login host with client credentials added.
func (o *OAuth) post(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	form.Set("client_id", o.cfg.ClientID)
	form.Set("client_secret", o.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(o.cfg.LoginURL, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("procore: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := o.hc.Do(req)
	if err != nil {
		// url.Error carries only the URL and cause; the secret is in the body.
		return nil, fmt.Errorf("procore: POST %s: %w", path, err)
	}
	return resp, nil
}

// statusError is a non-2xx upstream response with a truncated body.
type statusError struct {
	op     string
	status int
	body   string
}

func (e *statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("procore: %s failed with status %d", e.op, e.status)
	}
	return fmt.Sprintf("procore: %s failed with status %d: %s", e.op, e.status, e.body)
}

func newStatusError(op string, resp *http.Response) *statusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	return &statusError{op: op, status: resp.StatusCode, body: string(b)}
}
