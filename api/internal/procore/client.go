package procore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

// ValidationError is a 400 or 422 response whose body maps Procore field
// names to messages.
type ValidationError struct {
	Fields map[string][]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + strings.Join(e.Fields[k], "; ")
	}
	return "procore: validation failed: " + strings.Join(parts, ", ")
}

// parseValidationErrors reads {"errors": {field: "msg" | ["msg", ...] |
// {subfield: ...}}}. Nested objects flatten to dotted keys
// ({"question": {"body": [...]}} is "question.body"); any other leaf keeps
// its raw JSON as the message. It reports false when "errors" is missing,
// empty or not an object.
func parseValidationErrors(b []byte) (*ValidationError, bool) {
	var w struct {
		Errors map[string]json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(b, &w) != nil || len(w.Errors) == 0 {
		return nil, false
	}
	fields := map[string][]string{}
	flattenErrors("", w.Errors, fields)
	if len(fields) == 0 {
		return nil, false
	}
	return &ValidationError{Fields: fields}, true
}

func flattenErrors(prefix string, errs map[string]json.RawMessage, into map[string][]string) {
	for k, raw := range errs {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		var one string
		var many []string
		var nested map[string]json.RawMessage
		switch {
		case json.Unmarshal(raw, &one) == nil:
			into[key] = []string{one}
		case json.Unmarshal(raw, &many) == nil:
			into[key] = many
		case json.Unmarshal(raw, &nested) == nil && nested != nil:
			flattenErrors(key, nested, into)
		default:
			into[key] = []string{string(raw)}
		}
	}
}

// Client is a client for Procore's REST API.
type Client struct {
	apiURL string
	hc     *http.Client
	// uploadClient sends Direct File Upload bodies to the storage service:
	// hc's transport, a longer timeout and https-only redirects.
	uploadClient *http.Client
	sleep        func(time.Duration)
	now          func() time.Time
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
	uc := *hc
	uc.Timeout = uploadTimeout
	uc.CheckRedirect = httpsOnlyRedirect
	return &Client{apiURL: strings.TrimRight(apiURL, "/"), hc: hc, uploadClient: &uc, sleep: sleep, now: now}
}

// Me returns the user behind accessToken.
func (c *Client) Me(ctx context.Context, accessToken string) (Me, error) {
	var me Me
	err := c.do(ctx, accessToken, nil, http.MethodGet, "/rest/v1.0/me", nil, nil, &me)
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

func projectPath(projectID int64, suffix string) string {
	return "/rest/v1.0/projects/" + strconv.FormatInt(projectID, 10) + suffix
}

// Lookup kinds, as ProbeKind takes them; they match the pipeline's names.
const (
	KindUsers        = "users"
	KindLocations    = "locations"
	KindSpecSections = "spec_sections"
	KindCostCodes    = "cost_codes"
	KindVendors      = "vendors"
	KindDrawings     = "drawings"
	KindRFIManagers  = "rfi_managers"
)

// lookupEndpoint returns the list endpoint of a lookup kind: its path and
// query, without paging parameters.
func lookupEndpoint(kind string, projectID int64) (string, url.Values, bool) {
	byProject := url.Values{"project_id": {strconv.FormatInt(projectID, 10)}}
	switch kind {
	case KindUsers:
		return projectPath(projectID, "/users"), url.Values{}, true
	case KindLocations:
		return projectPath(projectID, "/locations"), url.Values{}, true
	case KindSpecSections:
		return projectPath(projectID, "/specification_sections"), url.Values{}, true
	case KindCostCodes:
		return "/rest/v1.0/cost_codes", byProject, true
	case KindVendors:
		return projectPath(projectID, "/vendors"), url.Values{}, true
	case KindDrawings:
		return "/rest/v1.1/drawings", byProject, true
	case KindRFIManagers:
		return projectPath(projectID, "/rfis/potential_rfi_managers"), url.Values{}, true
	}
	return "", nil, false
}

func listKind[T any](ctx context.Context, c *Client, token string, companyID, projectID int64, kind string) ([]T, error) {
	path, q, _ := lookupEndpoint(kind, projectID)
	return listAll[T](ctx, c, token, &companyID, path, q)
}

// ListProjectUsers returns every user on projectID, following pagination.
func (c *Client) ListProjectUsers(ctx context.Context, accessToken string, companyID, projectID int64) ([]ProjectUser, error) {
	return listKind[ProjectUser](ctx, c, accessToken, companyID, projectID, KindUsers)
}

// ListPotentialRFIManagers returns the users who may manage an RFI in
// projectID, following pagination. A 403 is ErrPermissionDenied; a project
// without the RFI tool answers 404.
func (c *Client) ListPotentialRFIManagers(ctx context.Context, accessToken string, companyID, projectID int64) ([]RFIManager, error) {
	return listKind[RFIManager](ctx, c, accessToken, companyID, projectID, KindRFIManagers)
}

// ListLocations returns every location in projectID, following pagination.
func (c *Client) ListLocations(ctx context.Context, accessToken string, companyID, projectID int64) ([]Location, error) {
	return listKind[Location](ctx, c, accessToken, companyID, projectID, KindLocations)
}

// ListSpecSections returns every specification section in projectID.
func (c *Client) ListSpecSections(ctx context.Context, accessToken string, companyID, projectID int64) ([]SpecSection, error) {
	return listKind[SpecSection](ctx, c, accessToken, companyID, projectID, KindSpecSections)
}

// ListCostCodes returns every cost code of projectID, following pagination.
func (c *Client) ListCostCodes(ctx context.Context, accessToken string, companyID, projectID int64) ([]CostCode, error) {
	return listKind[CostCode](ctx, c, accessToken, companyID, projectID, KindCostCodes)
}

// ListVendors returns every vendor of projectID, following pagination.
func (c *Client) ListVendors(ctx context.Context, accessToken string, companyID, projectID int64) ([]Vendor, error) {
	return listKind[Vendor](ctx, c, accessToken, companyID, projectID, KindVendors)
}

// ListDrawings returns every drawing of projectID, following pagination.
func (c *Client) ListDrawings(ctx context.Context, accessToken string, companyID, projectID int64) ([]Drawing, error) {
	return listKind[Drawing](ctx, c, accessToken, companyID, projectID, KindDrawings)
}

// ProbeKind checks that accessToken may read kind's list in projectID by
// requesting its first page with per_page=1 and discarding the body. It
// returns nil on a 2xx and the mapped error otherwise (ErrPermissionDenied
// on a 403).
func (c *Client) ProbeKind(ctx context.Context, accessToken string, companyID, projectID int64, kind string) error {
	path, q, ok := lookupEndpoint(kind, projectID)
	if !ok {
		return fmt.Errorf("procore: unknown lookup kind %q", kind)
	}
	q.Set("per_page", "1")
	q.Set("page", "1")
	var discard json.RawMessage
	return c.do(ctx, accessToken, &companyID, http.MethodGet, path, q, nil, &discard)
}

// GetProject returns projectID's detail, including its time zone. Procore's
// Show Project endpoint requires company_id in the query as well as the
// company header; without it the sandbox answers 400 "Missing Project or
// Company ID".
func (c *Client) GetProject(ctx context.Context, accessToken string, companyID, projectID int64) (ProjectDetail, error) {
	var p ProjectDetail
	q := url.Values{"company_id": {strconv.FormatInt(companyID, 10)}}
	err := c.do(ctx, accessToken, &companyID, http.MethodGet, projectPath(projectID, ""), q, nil, &p)
	return p, err
}

// CreateRFI posts a new RFI to projectID with Procore's configurable
// validations on. A 400 or 422 with field errors is returned as
// *ValidationError.
func (c *Client) CreateRFI(ctx context.Context, accessToken string, companyID, projectID int64, body RFICreate) (RFICreated, error) {
	var out RFICreated
	q := url.Values{"run_configurable_validations": {"true"}}
	err := c.do(ctx, accessToken, &companyID, http.MethodPost, projectPath(projectID, "/rfis"), q, map[string]RFICreate{"rfi": body}, &out)
	return out, err
}

// listAll requests successive pages of perPage items until a short page. A
// nil companyID omits the Procore-Company-Id header.
func listAll[T any](ctx context.Context, c *Client, token string, companyID *int64, path string, q url.Values) ([]T, error) {
	q.Set("per_page", strconv.Itoa(perPage))
	var all []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []T
		if err := c.do(ctx, token, companyID, http.MethodGet, path, q, nil, &items); err != nil {
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
// a 2xx JSON body into out. A non-nil body is sent as JSON.
func (c *Client) do(ctx context.Context, token string, companyID *int64, method, path string, q url.Values, body, out any) error {
	u := c.apiURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("procore: encode %s %s body: %w", method, path, err)
		}
	}
	for attempt := 0; ; attempt++ {
		// A fresh reader per attempt: a retry after 429 must resend the body.
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return fmt.Errorf("procore: build request: %w", err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
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

// decodeResponse decodes a 2xx body into out. Any other status reads up to
// maxErrorBodyParse of the body: a 400 or 422 whose body has a non-empty
// errors object is a *ValidationError (Procore reports some field problems,
// such as an invalid rfi_manager_id, with a 400); anything else is an
// *APIError carrying the body's first maxErrBody runes, wrapped in
// ErrUnauthorized on a 401 and ErrPermissionDenied on a 403.
func decodeResponse(ctx context.Context, resp *http.Response, method, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyParse))
		apiErr := &APIError{Status: resp.StatusCode, Body: errBodyPrefix(b)}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %w", ErrUnauthorized, apiErr)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrPermissionDenied, apiErr)
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			if ve, ok := parseValidationErrors(b); ok {
				return ve
			}
		}
		return apiErr
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return unavailable(ctx, fmt.Errorf("procore: decode %s %s response: %w", method, path, err))
	}
	return nil
}

// errBodyPrefix is the start of an error body kept on an APIError: its
// first maxErrBody runes.
func errBodyPrefix(b []byte) string {
	s := string(b)
	n := 0
	for i := range s {
		if n == maxErrBody {
			return s[:i]
		}
		n++
	}
	return s
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
