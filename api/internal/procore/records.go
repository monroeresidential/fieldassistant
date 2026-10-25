package procore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
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
// DateTime-with-UTC-offset format in Procore's RFI reference; it has not been
// exercised against the sandbox yet. A nil updatedSince lists every RFI.
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

// GetRFIRaw returns Procore's Show RFI body for rfiID verbatim.
func (c *Client) GetRFIRaw(ctx context.Context, accessToken string, companyID, projectID, rfiID int64) (json.RawMessage, error) {
	var body json.RawMessage
	path := projectPath(projectID, "/rfis/"+strconv.FormatInt(rfiID, 10))
	if err := c.do(ctx, accessToken, &companyID, http.MethodGet, path, nil, nil, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// FetchURL GETs rawURL with the client's http.Client and none of the Procore
// headers, for the pre-signed file URLs Procore returns. The caller closes the
// response body and checks the status.
func (c *Client) FetchURL(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("procore: build file request: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, unavailable(ctx, fmt.Errorf("procore: fetch file: %w", err))
	}
	return resp, nil
}
