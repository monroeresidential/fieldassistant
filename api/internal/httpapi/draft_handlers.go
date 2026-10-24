package httpapi

import "context"

// The draft and job operations are stubs until the drafts pipeline and the
// job queue exist; each answers 500 through the strict error handler.

// CreateDraft is not implemented yet.
func (s *Server) CreateDraft(context.Context, CreateDraftRequestObject) (CreateDraftResponseObject, error) {
	return nil, errNotImplemented
}

// ListDrafts is not implemented yet.
func (s *Server) ListDrafts(context.Context, ListDraftsRequestObject) (ListDraftsResponseObject, error) {
	return nil, errNotImplemented
}

// GetDraft is not implemented yet.
func (s *Server) GetDraft(context.Context, GetDraftRequestObject) (GetDraftResponseObject, error) {
	return nil, errNotImplemented
}

// SaveDraft is not implemented yet.
func (s *Server) SaveDraft(context.Context, SaveDraftRequestObject) (SaveDraftResponseObject, error) {
	return nil, errNotImplemented
}

// SubmitDraft is not implemented yet.
func (s *Server) SubmitDraft(context.Context, SubmitDraftRequestObject) (SubmitDraftResponseObject, error) {
	return nil, errNotImplemented
}

// GetJob is not implemented yet.
func (s *Server) GetJob(context.Context, GetJobRequestObject) (GetJobResponseObject, error) {
	return nil, errNotImplemented
}
