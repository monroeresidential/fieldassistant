package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
)

// recordsSyncTimeout bounds the Procore pull GetRecords runs before reading
// the mirror, so a slow Procore still gets the user the page they have.
const recordsSyncTimeout = 30 * time.Second

// attachmentFetchTimeout bounds fetching an attachment from Procore and
// streaming it to the phone, which can be slow for a drawing-sized file on
// site signal.
const attachmentFetchTimeout = 5 * time.Minute

// GetRecords pulls the project's records of one kind into the user's mirror
// when they are stale (always with Cache-Control: no-cache), then returns the
// user's changes after the since cursor. A failed pull is reported in pull,
// not as an error, and the page is served from what the mirror has.
func (s *Server) GetRecords(ctx context.Context, req GetRecordsRequestObject) (GetRecordsResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	kind, ok := records.ParseKind(string(req.Kind))
	if !ok {
		return nil, badRequest("unknown record kind " + string(req.Kind))
	}
	force := req.Params.CacheControl != nil && hasNoCache(*req.Params.CacheControl)
	limit := records.MaxPageLimit
	if req.Params.Limit != nil {
		limit = max(1, min(int(*req.Params.Limit), records.MaxPageLimit))
	}
	var since int64
	if req.Params.Since != nil {
		since = *req.Params.Since
	}

	syncCtx, cancel := context.WithTimeout(ctx, recordsSyncTimeout)
	status := s.deps.Records.Sync(syncCtx, userID, companyID, req.ProjectId, kind, records.SyncOpts{Force: force})
	cancel()

	page, err := s.deps.Records.Delta(ctx, userID, req.ProjectId, kind, since, limit)
	if err != nil {
		return nil, err
	}
	envs := make([]RecordEnvelope, 0, len(page.Records))
	for _, r := range page.Records {
		env := RecordEnvelope{Id: r.ID, Kind: string(r.Kind), UpdatedAt: r.UpdatedAt, ChangeSeq: r.ChangeSeq}
		switch r.Kind {
		case records.KindRFI:
			rec, detailErr, err := decodeRFIRecord(req.ProjectId, r.Body)
			if err != nil {
				// The cursor moves past this record; it reaches the phone
				// only once its body changes in Procore.
				s.logger().Error("skipping undecodable record", "kind", r.Kind, "id", r.ID, "error", err, "detail_error", detailErr)
				continue
			}
			if detailErr != nil {
				s.logger().Error("record shipped with minimal fields", "kind", r.Kind, "id", r.ID, "error", detailErr)
			}
			env.Rfi = &rec
		}
		envs = append(envs, env)
	}
	return GetRecords200JSONResponse{
		Records:    envs,
		DeletedIds: page.DeletedIDs,
		NextSince:  page.NextSince,
		HasMore:    page.HasMore,
		SyncedAt:   page.SyncedAt,
		Pull:       pullStatus(status),
	}, nil
}

// SyncProject pulls each requested kind now, forced, and fully when the body
// asks, and reports each pull's outcome.
func (s *Server) SyncProject(ctx context.Context, req SyncProjectRequestObject) (SyncProjectResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("sync body is required")
	}
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	if len(req.Body.Kinds) == 0 {
		return nil, badRequest("kinds must name at least one record kind")
	}
	kinds := make([]records.Kind, 0, len(req.Body.Kinds))
	for _, k := range req.Body.Kinds {
		kind, ok := records.ParseKind(string(k))
		if !ok {
			return nil, badRequest("unknown record kind " + string(k))
		}
		kinds = append(kinds, kind)
	}
	opts := records.SyncOpts{Force: true, Full: req.Body.Full != nil && *req.Body.Full}
	pulls := make([]PullStatus, 0, len(kinds))
	for _, kind := range kinds {
		pulls = append(pulls, pullStatus(s.deps.Records.Sync(ctx, userID, companyID, req.ProjectId, kind, opts)))
	}
	return SyncProject200JSONResponse{Pulls: pulls}, nil
}

// GetRFIAttachment streams one of an RFI's question or answer attachments
// through the backend. The RFI is read live from Procore with the user's
// token, so the attachment must be on an RFI they can see now.
func (s *Server) GetRFIAttachment(ctx context.Context, req GetRFIAttachmentRequestObject) (GetRFIAttachmentResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	token, err := s.deps.Tokens.AccessToken(ctx, userID)
	if err != nil {
		return nil, s.procoreFailure("rfi attachment", err)
	}
	raw, err := s.deps.Procore.GetRFIRaw(ctx, token, companyID, req.ProjectId, req.RfiId)
	if err != nil {
		var pcErr *procore.APIError
		if errors.As(err, &pcErr) && pcErr.Status == http.StatusNotFound {
			return nil, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "RFI not found"}
		}
		return nil, s.procoreFailure("rfi attachment", err)
	}
	var d procore.RFIDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		s.logger().Warn("undecodable procore rfi", "id", req.RfiId, "error", err)
		return nil, &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "Procore returned an unreadable RFI"}
	}
	att, ok := findRFIAttachment(d, req.AttachmentId)
	if !ok {
		return nil, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "attachment is not on this RFI"}
	}

	// Only an absolute https URL is fetched; FetchURL keeps redirects to
	// https too.
	if u, err := url.Parse(att.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		s.logger().Warn("unusable attachment location", "rfi_id", req.RfiId, "attachment_id", req.AttachmentId)
		return nil, &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "attachment location is not usable"}
	}
	// The fetch and the copy to the phone share one deadline, released when
	// the generated visitor closes the body after copying.
	fetchCtx, cancel := context.WithTimeout(ctx, attachmentFetchTimeout)
	resp, err := s.deps.Procore.FetchURL(fetchCtx, att.URL)
	if err != nil {
		cancel()
		return nil, s.procoreFailure("rfi attachment file", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		s.logger().Warn("procore file fetch failed", "op", "rfi attachment file", "status", resp.StatusCode)
		return nil, &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "Procore file download failed"}
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = att.ContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := att.Filename
	if filename == "" {
		filename = att.Name
	}
	// The generated visitor omits Content-Length when it is 0, so an
	// unknown length (-1) is passed as 0.
	length := max(resp.ContentLength, 0)
	return nosniffAttachment{GetRFIAttachment200ApplicationoctetStreamResponse{
		Body:          cancelOnClose{ReadCloser: resp.Body, cancel: cancel},
		ContentLength: length,
		Headers: GetRFIAttachment200ResponseHeaders{
			ContentType:        contentType,
			ContentDisposition: `attachment; filename="` + quoteFilename(filename) + `"`,
		},
	}}, nil
}

// cancelOnClose releases a fetch context when its response body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// nosniffAttachment is the generated 200 attachment response with
// X-Content-Type-Options: nosniff added, so a client never sniffs
// Procore-supplied bytes into a different type than the one declared.
type nosniffAttachment struct {
	GetRFIAttachment200ApplicationoctetStreamResponse
}

func (r nosniffAttachment) VisitGetRFIAttachmentResponse(w http.ResponseWriter) error {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return r.GetRFIAttachment200ApplicationoctetStreamResponse.VisitGetRFIAttachmentResponse(w)
}

// findRFIAttachment finds attachment id among the RFI's questions and answers.
func findRFIAttachment(d procore.RFIDetail, id int64) (procore.RFIAttachmentDetail, bool) {
	for _, q := range d.Questions {
		for _, a := range q.Attachments {
			if a.ID == id {
				return a, true
			}
		}
		for _, ans := range q.Answers {
			for _, a := range ans.Attachments {
				if a.ID == id {
					return a, true
				}
			}
		}
	}
	return procore.RFIAttachmentDetail{}, false
}

// quoteFilename escapes a filename for a quoted Content-Disposition
// parameter: backslashes and quotes are escaped and control characters,
// which a header value cannot carry, are dropped.
func quoteFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case r == '"' || r == '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pullStatus maps a mirror pull outcome to the API's PullStatus; code and
// message are set only for a failed pull.
func pullStatus(p records.PullStatus) PullStatus {
	out := PullStatus{Kind: string(p.Kind), Status: PullStatusStatus(p.Status), PulledAt: p.PulledAt}
	if p.Status == records.StatusFailed {
		code, msg := p.Code, p.Message
		out.Code, out.Message = &code, &msg
	}
	return out
}
