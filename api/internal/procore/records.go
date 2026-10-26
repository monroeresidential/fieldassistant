package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
)

// RawRecord is one Procore list item kept verbatim, with the two fields the
// sync needs pulled out of it.
type RawRecord struct {
	ID        int64
	UpdatedAt time.Time
	Body      json.RawMessage
}

// ListRFIsRaw returns every RFI in projectID as raw JSON, following
// pagination. With updatedSince it sends filters[updated_at] as Procore's
// documented ISO 8601 range "<RFC3339 start>...<RFC3339 end>" in UTC, ending
// now, so only RFIs changed since then come back. The three-dot form is the
// DateTime-with-UTC-offset format in Procore's RFI reference; the sandbox
// accepted it on 2026-10-25. A nil updatedSince lists every RFI. The bodies
// are list items; see ListRFIsDetailed for full ones.
func (c *Client) ListRFIsRaw(ctx context.Context, accessToken string, companyID, projectID int64, updatedSince *time.Time) ([]RawRecord, error) {
	q := url.Values{}
	if updatedSince != nil {
		q.Set("filters[updated_at]", updatedSince.UTC().Format(time.RFC3339)+"..."+c.now().UTC().Format(time.RFC3339))
	}
	items, err := listAll[json.RawMessage](ctx, c, accessToken, &companyID, projectPath(projectID, "/rfis"), q)
	if err != nil {
		return nil, err
	}
	out := make([]RawRecord, len(items))
	for i, raw := range items {
		var h struct {
			ID        int64     `json:"id"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, unavailable(ctx, fmt.Errorf("procore: decode rfi list item: %w", err))
		}
		out[i] = RawRecord{ID: h.ID, UpdatedAt: h.UpdatedAt, Body: raw}
	}
	return out, nil
}

// showConcurrency bounds the Show RFI requests ListRFIsDetailed has in
// flight at once. Procore's per-token rate limit is generous for this (its
// 429 handling in do still applies); the bound keeps one user's full sweep
// from flooding the connection pool.
const showConcurrency = 4

// ListRFIsDetailed lists RFIs like ListRFIsRaw, then replaces each item's
// body with its Show RFI response. Procore's list items carry a question as
// {id, body} only (verified against the sandbox 2026-10-25): no plain text,
// attachments or answers, which the mirror promises the phone. ID and
// UpdatedAt stay the list's, so the delta high-water mark matches what the
// list filter will return next time. An RFI that disappears between the
// list and its Show (404) is dropped; any other Show failure fails the
// whole call, so a pull never stores a half-refreshed set as complete.
func (c *Client) ListRFIsDetailed(ctx context.Context, accessToken string, companyID, projectID int64, updatedSince *time.Time) ([]RawRecord, error) {
	items, err := c.ListRFIsRaw(ctx, accessToken, companyID, projectID, updatedSince)
	if err != nil {
		return nil, err
	}
	bodies := make([]json.RawMessage, len(items))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(showConcurrency)
	for i := range items {
		g.Go(func() error {
			body, err := c.GetRFIRaw(gctx, accessToken, companyID, projectID, items[i].ID)
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				return nil // gone since the list; the next sweep deletes it
			}
			if err != nil {
				return err
			}
			bodies[i] = body
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := items[:0]
	for i, item := range items {
		if bodies[i] == nil {
			continue
		}
		item.Body = bodies[i]
		out = append(out, item)
	}
	return out, nil
}

// GetRFIRaw returns Procore's Show RFI body for rfiID verbatim.
func (c *Client) GetRFIRaw(ctx context.Context, accessToken string, companyID, projectID, rfiID int64) (json.RawMessage, error) {
	var body json.RawMessage
	path := projectPath(projectID, "/rfis/"+strconv.FormatInt(rfiID, 10))
	if err := c.do(ctx, accessToken, &companyID, http.MethodGet, path, nil, nil, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// errInsecureRedirect refuses a file redirect to anything but https.
var errInsecureRedirect = errors.New("procore: file redirect is not https")

// maxFileRedirects matches net/http's default redirect limit.
const maxFileRedirects = 10

// FetchURL GETs rawURL with the client's http.Client and none of the Procore
// headers, for the pre-signed file URLs Procore returns. It follows
// redirects only to https URLs. A URL that cannot be requested, a refused
// redirect and a transport failure are all ErrUnavailable. The caller
// checks the scheme of rawURL itself, closes the response body and checks
// the status.
//
// The client's Timeout is dropped for this request: http.Client.Timeout
// also bounds reading the body, and a large file streamed to a phone on a
// weak signal can take longer than an API call. ctx bounds the whole fetch
// instead, body included; the transport's dial and TLS limits still apply.
func (c *Client) FetchURL(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, unavailable(ctx, fmt.Errorf("procore: build file request: %w", err))
	}
	hc := *c.hc
	hc.Timeout = 0
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return errInsecureRedirect
		}
		if len(via) >= maxFileRedirects {
			return errors.New("procore: too many file redirects")
		}
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, unavailable(ctx, fmt.Errorf("procore: fetch file: %w", err))
	}
	return resp, nil
}
