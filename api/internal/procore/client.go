package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnauthorized means Procore rejected the access token (HTTP 401).
	ErrUnauthorized = errors.New("procore unauthorized")
	// ErrPermissionDenied means the user lacks access to the resource (HTTP 403).
	ErrPermissionDenied = errors.New("procore permission denied")
	// ErrUnavailable means Procore could not be reached or sent a body that
	// does not decode. A caller's own cancellation is not reported this way.
	ErrUnavailable = errors.New("procore unavailable")
)

const (
	perPage         = 100
	maxPages        = 50
	maxRateRetries  = 3
	defaultRateWait = time.Second
	maxRateWait     = 60 * time.Second
)

// APIError is a non-2xx Procore REST response with a truncated body.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("procore: API status %d", e.Status)
	}
	return fmt.Sprintf("procore: API status %d: %s", e.Status, e.Body)
}

// Client is a client for Procore's REST API.
type Client struct {
	apiURL string
	hc     *http.Client
	sleep  func(time.Duration)
	now    func() time.Time
}

// NewClient builds a REST client. A nil hc uses http.DefaultClient, a nil
// sleep uses time.Sleep and a nil now uses time.Now.
func NewClient(apiURL string, hc *http.Client, sleep func(time.Duration), now func() time.Time) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	if now == nil {
		now = time.Now
	}
	return &Client{apiURL: strings.TrimRight(apiURL, "/"), hc: hc, sleep: sleep, now: now}
}

// Me returns the user behind accessToken.
func (c *Client) Me(ctx context.Context, accessToken string) (Me, error) {
	var me Me
	err := c.do(ctx, accessToken, nil, http.MethodGet, "/rest/v1.0/me", nil, &me)
	return me, err
}

// ListCompanies returns every company the user can access, following pagination.
func (c *Client) ListCompanies(ctx context.Context, accessToken string) ([]Company, error) {
	return listAll[Company](ctx, c, accessToken, nil, "/rest/v1.0/companies", url.Values{})
}

// ListProjects returns every project in companyID, following pagination.
func (c *Client) ListProjects(ctx context.Context, accessToken string, companyID int64) ([]Project, error) {
	q := url.Values{"company_id": {strconv.FormatInt(companyID, 10)}}
	return listAll[Project](ctx, c, accessToken, &companyID, "/rest/v1.0/projects", q)
}

// ListRFIs returns every RFI in projectID, following pagination.
func (c *Client) ListRFIs(ctx context.Context, accessToken string, companyID, projectID int64) ([]RFI, error) {
	path := "/rest/v1.0/projects/" + strconv.FormatInt(projectID, 10) + "/rfis"
	return listAll[RFI](ctx, c, accessToken, &companyID, path, url.Values{})
}

// listAll requests successive pages of perPage items until a short page. A
// nil companyID omits the Procore-Company-Id header.
func listAll[T any](ctx context.Context, c *Client, token string, companyID *int64, path string, q url.Values) ([]T, error) {
	q.Set("per_page", strconv.Itoa(perPage))
	var all []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []T
		if err := c.do(ctx, token, companyID, http.MethodGet, path, q, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < perPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("procore: GET %s exceeded %d pages", path, maxPages)
}

// do sends one request, retrying on 429, mapping error statuses and decoding
// a 2xx JSON body into out.
func (c *Client) do(ctx context.Context, token string, companyID *int64, method, path string, q url.Values, out any) error {
	u := c.apiURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return fmt.Errorf("procore: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if companyID != nil {
			req.Header.Set("Procore-Company-Id", strconv.FormatInt(*companyID, 10))
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			// url.Error carries only the URL and cause; the token is in a header.
			return unavailable(ctx, fmt.Errorf("procore: %s %s: %w", method, path, err))
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRateRetries {
			wait := c.rateWait(resp.Header.Get("X-Rate-Limit-Reset"))
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			c.sleep(wait)
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		return decodeResponse(ctx, resp, method, path, out)
	}
}

// unavailable wraps err with ErrUnavailable unless ctx is done, in which
// case the failure is the caller's cancellation and err is returned as is.
func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func decodeResponse(ctx context.Context, resp *http.Response, method, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		apiErr := &APIError{Status: resp.StatusCode, Body: string(b)}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %w", ErrUnauthorized, apiErr)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrPermissionDenied, apiErr)
		}
		return apiErr
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return unavailable(ctx, fmt.Errorf("procore: decode %s %s response: %w", method, path, err))
	}
	return nil
}

// rateWait turns an X-Rate-Limit-Reset unix timestamp into a bounded wait.
func (c *Client) rateWait(reset string) time.Duration {
	sec, err := strconv.ParseInt(strings.TrimSpace(reset), 10, 64)
	if err != nil {
		return defaultRateWait
	}
	wait := time.Unix(sec, 0).Sub(c.now())
	if wait < 0 {
		return 0
	}
	return min(wait, maxRateWait)
}
