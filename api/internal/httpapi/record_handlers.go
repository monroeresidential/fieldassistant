package httpapi

import (
	"context"
	"net/http"
)

var errNotImplemented = &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "not implemented"}

// GetRecords is a stub until the records handlers land.
func (s *Server) GetRecords(_ context.Context, _ GetRecordsRequestObject) (GetRecordsResponseObject, error) {
	return nil, errNotImplemented
}

// SyncProject is a stub until the records handlers land.
func (s *Server) SyncProject(_ context.Context, _ SyncProjectRequestObject) (SyncProjectResponseObject, error) {
	return nil, errNotImplemented
}

// GetRFIAttachment is a stub until the records handlers land.
func (s *Server) GetRFIAttachment(_ context.Context, _ GetRFIAttachmentRequestObject) (GetRFIAttachmentResponseObject, error) {
	return nil, errNotImplemented
}
