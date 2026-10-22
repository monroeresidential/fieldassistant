package httpapi

import "context"

// Server implements StrictServerInterface.
type Server struct {
	deps Deps
}

var _ StrictServerInterface = (*Server)(nil)

func (s *Server) AuthStart(ctx context.Context, req AuthStartRequestObject) (AuthStartResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AuthCallback(ctx context.Context, req AuthCallbackRequestObject) (AuthCallbackResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AuthExchange(ctx context.Context, req AuthExchangeRequestObject) (AuthExchangeResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AuthRefresh(ctx context.Context, req AuthRefreshRequestObject) (AuthRefreshResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) AuthLogout(ctx context.Context, req AuthLogoutRequestObject) (AuthLogoutResponseObject, error) {
	return nil, errNotImplemented
}

func (s *Server) GetMe(ctx context.Context, req GetMeRequestObject) (GetMeResponseObject, error) {
	return nil, errNotImplemented
}

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
