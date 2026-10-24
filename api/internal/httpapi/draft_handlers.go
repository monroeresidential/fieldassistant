package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

var (
	errDraftNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "draft not found"}
	errDraftLocked   = &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "draft is submitted or being submitted"}
	errDraftChanged  = &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "draft changed while submitting; try again"}
	// errExtractFailed uses procore_unavailable because the error code set has
	// no LLM-specific code; the message tells the two apart.
	errExtractFailed = &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "extraction failed"}
)

// draftFailure maps pipeline errors to API errors, then Procore and token
// errors through procoreFailure.
func (s *Server) draftFailure(op string, err error) error {
	switch {
	case errors.Is(err, pipeline.ErrNotFound):
		return errDraftNotFound
	case errors.Is(err, pipeline.ErrDraftLocked):
		return errDraftLocked
	case errors.Is(err, pipeline.ErrDraftChanged):
		return errDraftChanged
	case errors.Is(err, pipeline.ErrExtractFailed):
		s.logger().Warn("draft extraction failed", "op", op, "error", err)
		return errExtractFailed
	}
	return s.procoreFailure(op, err)
}

// CreateDraft extracts a draft from a transcript, or returns the existing
// draft with the same id.
func (s *Server) CreateDraft(ctx context.Context, req CreateDraftRequestObject) (CreateDraftResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("draft body is required")
	}
	if !req.Body.Feature.Valid() {
		return nil, badRequest("unknown feature " + string(req.Body.Feature))
	}
	if strings.TrimSpace(req.Body.Transcript) == "" {
		return nil, badRequest("transcript is required")
	}
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	d, err := s.deps.Drafts.Create(ctx, pipeline.CreateInput{
		ID:         req.Body.Id,
		UserID:     userID,
		CompanyID:  companyID,
		ProjectID:  req.Body.ProjectId,
		Feature:    string(req.Body.Feature),
		Transcript: req.Body.Transcript,
		CapturedAt: req.Body.CapturedAt,
	})
	if err != nil {
		return nil, s.draftFailure("create draft", err)
	}
	body, err := draftResponse(d)
	if err != nil {
		return nil, err
	}
	return CreateDraft200JSONResponse(body), nil
}

// ListDrafts returns the user's drafts in a project, newest first.
func (s *Server) ListDrafts(ctx context.Context, req ListDraftsRequestObject) (ListDraftsResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	limit := 50
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	drafts, err := s.deps.Drafts.List(ctx, userID, req.Params.ProjectId, limit)
	if err != nil {
		return nil, err
	}
	out := make(ListDrafts200JSONResponse, 0, len(drafts))
	for _, d := range drafts {
		if d.CompanyID != companyID {
			continue
		}
		body, err := draftResponse(d)
		if err != nil {
			return nil, err
		}
		out = append(out, body)
	}
	return out, nil
}

// GetDraft returns one of the user's drafts in the company.
func (s *Server) GetDraft(ctx context.Context, req GetDraftRequestObject) (GetDraftResponseObject, error) {
	d, err := s.companyDraft(ctx, req.Params.XCompanyId, req.DraftId)
	if err != nil {
		return nil, err
	}
	body, err := draftResponse(d)
	if err != nil {
		return nil, err
	}
	return GetDraft200JSONResponse(body), nil
}

// SaveDraft stores the reviewer's payload, revalidated against the lookups.
func (s *Server) SaveDraft(ctx context.Context, req SaveDraftRequestObject) (SaveDraftResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("draft body is required")
	}
	if _, err := s.companyDraft(ctx, req.Params.XCompanyId, req.DraftId); err != nil {
		return nil, err
	}
	p := req.Body.Payload
	if p.AssigneeIds == nil {
		p.AssigneeIds = []int64{}
	}
	if p.DistributionIds == nil {
		p.DistributionIds = []int64{}
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	d, err := s.deps.Drafts.SaveEdits(ctx, claimsFrom(ctx).UserID, req.DraftId, payload)
	if err != nil {
		return nil, s.draftFailure("save draft", err)
	}
	body, err := draftResponse(d)
	if err != nil {
		return nil, err
	}
	return SaveDraft200JSONResponse(body), nil
}

// companyDraft loads the user's draft after checking the company is visible.
// A draft in another company is reported as not found.
func (s *Server) companyDraft(ctx context.Context, companyID int64, id uuid.UUID) (pipeline.Draft, error) {
	userID := claimsFrom(ctx).UserID
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return pipeline.Draft{}, err
	}
	d, err := s.deps.Drafts.Get(ctx, userID, id)
	if err != nil {
		return pipeline.Draft{}, s.draftFailure("get draft", err)
	}
	if d.CompanyID != companyID {
		return pipeline.Draft{}, errDraftNotFound
	}
	return d, nil
}

// draftResponse converts a pipeline draft to the contract shape. Required
// arrays and confidence are never null.
func draftResponse(d pipeline.Draft) (DraftResponse, error) {
	var payload RFIDraftPayload
	if raw := d.Payload(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return DraftResponse{}, fmt.Errorf("decode draft %s payload: %w", d.ID, err)
		}
	}
	if payload.AssigneeIds == nil {
		payload.AssigneeIds = []int64{}
	}
	if payload.DistributionIds == nil {
		payload.DistributionIds = []int64{}
	}

	unresolved := make([]UnresolvedField, 0, len(d.Unresolved))
	for _, u := range d.Unresolved {
		cands := make([]LookupRow, 0, len(u.Candidates))
		for _, c := range u.Candidates {
			row := LookupRow{Id: c.ID, Label: c.Label}
			if len(c.Extra) > 0 {
				extra := c.Extra
				row.Extra = &extra
			}
			cands = append(cands, row)
		}
		f := UnresolvedField{Field: u.Field, Reason: UnresolvedFieldReason(u.Reason), Candidates: cands}
		if u.Message != "" {
			msg := u.Message
			f.Message = &msg
		}
		unresolved = append(unresolved, f)
	}

	confidence := d.Confidence
	if confidence == nil {
		confidence = map[string]float64{}
	}
	out := DraftResponse{
		Id:         d.ID,
		Feature:    DraftResponseFeature(d.Feature),
		ProjectId:  d.ProjectID,
		Status:     DraftResponseStatus(d.Status),
		Payload:    payload,
		Unresolved: unresolved,
		Confidence: confidence,
		Transcript: d.Transcript,
		CreatedAt:  d.CreatedAt,
		UpdatedAt:  d.UpdatedAt,
	}
	if d.ModelVersion != "" {
		mv := d.ModelVersion
		out.ModelVersion = &mv
	}
	if d.Result != nil {
		r := DraftResult{ProcoreId: d.Result.ProcoreID}
		if d.Result.Number != "" {
			n := d.Result.Number
			r.Number = &n
		}
		if d.Result.URL != "" {
			u := d.Result.URL
			r.Url = &u
		}
		out.Result = &r
	}
	if d.Error != nil {
		out.Error = &Error{Code: d.Error.Code, Message: d.Error.Message}
	}
	return out, nil
}
