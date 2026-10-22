package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// rfiPermissionMessage replaces the generic permission message on the RFI
// route, where a 403 almost always means the RFI tool is off for the user.
const rfiPermissionMessage = "Procore permission denied for the RFI tool"

// procoreDateLayout is the format of Procore date-only fields such as an
// RFI's due_date.
const procoreDateLayout = "2006-01-02"

var errForbiddenScope = &APIError{
	Status: http.StatusForbidden, Code: "forbidden_scope", Message: "company is not visible to this user",
}

// mapProcoreErr turns token source and Procore client errors into API
// errors. It returns nil for any other error, which the caller passes on
// unchanged to become a 500.
func mapProcoreErr(err error) *APIError {
	var pcErr *procore.APIError
	switch {
	case errors.Is(err, auth.ErrReauthRequired), errors.Is(err, procore.ErrUnauthorized):
		return &APIError{Status: http.StatusUnauthorized, Code: "reauth_required", Message: "sign in to Procore again"}
	case errors.Is(err, procore.ErrPermissionDenied):
		return &APIError{Status: http.StatusForbidden, Code: "procore_permission_denied", Message: "Procore permission denied"}
	case errors.Is(err, procore.ErrUnavailable), errors.As(err, &pcErr):
		return &APIError{Status: http.StatusBadGateway, Code: "procore_unavailable", Message: "Procore request failed"}
	}
	return nil
}

// procoreFailure is mapProcoreErr for handler returns: the mapped API error
// when there is one, otherwise err itself.
func procoreFailure(err error) error {
	if apiErr := mapProcoreErr(err); apiErr != nil {
		return apiErr
	}
	return err
}

// requireVisible returns errForbiddenScope unless companyID is one of the
// user's Procore companies.
func (s *Server) requireVisible(ctx context.Context, userID uuid.UUID, companyID int64) error {
	visible, err := s.deps.Companies.Visible(ctx, userID, companyID)
	if err != nil {
		return procoreFailure(err)
	}
	if !visible {
		return errForbiddenScope
	}
	return nil
}

// PutMeSelection saves the user's last company and project. The company must
// be visible to the user; the project is not checked against Procore.
func (s *Server) PutMeSelection(ctx context.Context, req PutMeSelectionRequestObject) (PutMeSelectionResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("selection body is required")
	}
	userID := claimsFrom(ctx).UserID
	if err := s.requireVisible(ctx, userID, req.Body.CompanyId); err != nil {
		return nil, err
	}
	err := s.deps.Store.Q.SetUserSelection(ctx, db.SetUserSelectionParams{
		ID:            pgtype.UUID{Bytes: userID, Valid: true},
		LastCompanyID: pgtype.Int8{Int64: req.Body.CompanyId, Valid: true},
		LastProjectID: pgtype.Int8{Int64: req.Body.ProjectId, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	return PutMeSelection204Response{}, nil
}

// ListCompanies returns the user's Procore companies.
func (s *Server) ListCompanies(ctx context.Context, _ ListCompaniesRequestObject) (ListCompaniesResponseObject, error) {
	companies, err := s.deps.Companies.Companies(ctx, claimsFrom(ctx).UserID)
	if err != nil {
		return nil, procoreFailure(err)
	}
	out := make(ListCompanies200JSONResponse, 0, len(companies))
	for _, c := range companies {
		out = append(out, Company{Id: c.ID, Name: c.Name})
	}
	return out, nil
}

// ListProjects returns the projects in a company visible to the user.
func (s *Server) ListProjects(ctx context.Context, req ListProjectsRequestObject) (ListProjectsResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	if err := s.requireVisible(ctx, userID, req.CompanyId); err != nil {
		return nil, err
	}
	token, err := s.deps.Tokens.AccessToken(ctx, userID)
	if err != nil {
		return nil, procoreFailure(err)
	}
	projects, err := s.deps.Procore.ListProjects(ctx, token, req.CompanyId)
	if err != nil {
		return nil, procoreFailure(err)
	}
	out := make(ListProjects200JSONResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, Project{Id: p.ID, Name: p.Name, DisplayName: p.DisplayName})
	}
	return out, nil
}

// ListRFIs returns the RFIs in a project of the company named by X-Company-Id.
func (s *Server) ListRFIs(ctx context.Context, req ListRFIsRequestObject) (ListRFIsResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	token, err := s.deps.Tokens.AccessToken(ctx, userID)
	if err != nil {
		return nil, procoreFailure(err)
	}
	rfis, err := s.deps.Procore.ListRFIs(ctx, token, companyID, req.ProjectId)
	if err != nil {
		apiErr := mapProcoreErr(err)
		if apiErr == nil {
			return nil, err
		}
		if apiErr.Code == "procore_permission_denied" {
			apiErr.Message = rfiPermissionMessage
		}
		return nil, apiErr
	}
	out := make(ListRFIs200JSONResponse, 0, len(rfis))
	for _, r := range rfis {
		out = append(out, rfiSummary(r))
	}
	return out, nil
}

// rfiSummary maps a Procore RFI to the API's summary: assignees by name only,
// and due_date nil when Procore's is absent or not a YYYY-MM-DD date.
func rfiSummary(r procore.RFI) RFISummary {
	names := make([]string, 0, len(r.Assignees))
	for _, a := range r.Assignees {
		names = append(names, a.Name)
	}
	var due *openapi_types.Date
	if r.DueDate != nil {
		if t, err := time.Parse(procoreDateLayout, *r.DueDate); err == nil {
			due = &openapi_types.Date{Time: t}
		}
	}
	return RFISummary{
		Id:        r.ID,
		Number:    r.Number,
		Subject:   r.Subject,
		Status:    r.Status,
		DueDate:   due,
		Assignees: names,
	}
}
