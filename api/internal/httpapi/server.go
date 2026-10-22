package httpapi

import "context"

// Server implements StrictServerInterface.
type Server struct {
	deps Deps
}

var _ StrictServerInterface = (*Server)(nil)

func (s *Server) PutMeSelection(ctx context.Context, req PutMeSelectionRequestObject) (PutMeSelectionResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListCompanies(ctx context.Context, req ListCompaniesRequestObject) (ListCompaniesResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListProjects(ctx context.Context, req ListProjectsRequestObject) (ListProjectsResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) ListRFIs(ctx context.Context, req ListRFIsRequestObject) (ListRFIsResponseObject, error) {
	return nil, errNotImplemented
}
