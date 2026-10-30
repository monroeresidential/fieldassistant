package httpapi

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

var (
	errAttachmentNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "attachment is not on this draft"}
	errAttachmentInvalid  = &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "photo must be a JPEG under 20 MiB"}
	errTooManyAttachments = &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "at most 10 photos"}
)

// attachmentFailure maps attachment errors to API errors, and the rest
// through draftFailure. It is checked before draftFailure because
// ErrAttachmentNotFound wraps ErrNotFound, which draftFailure reports as a
// missing draft.
func (s *Server) attachmentFailure(op string, err error) error {
	var pending *pipeline.PendingError
	switch {
	case errors.Is(err, pipeline.ErrAttachmentNotFound):
		return errAttachmentNotFound
	case errors.Is(err, pipeline.ErrAttachmentInvalid):
		return errAttachmentInvalid
	case errors.Is(err, pipeline.ErrTooManyAttachments):
		return errTooManyAttachments
	case errors.As(err, &pending):
		return badRequest(pending.Error())
	}
	return s.draftFailure(op, err)
}

// contentTypeKey holds the request's Content-Type in the context.
type contentTypeKey struct{}

// keepContentType is a strict middleware that puts the request's
// Content-Type in the context of putDraftAttachment, whose request object
// carries only the body.
func keepContentType(f StrictHandlerFunc, operationID string) StrictHandlerFunc {
	if operationID != "PutDraftAttachment" {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		return f(context.WithValue(ctx, contentTypeKey{}, r.Header.Get("Content-Type")), w, r, req)
	}
}

func contentTypeFrom(ctx context.Context) string {
	ct, _ := ctx.Value(contentTypeKey{}).(string)
	return ct
}

// PutDraftAttachment stores the JPEG bytes of a photo on the user's draft.
// The body goes to the pipeline unread; it limits and checks it.
func (s *Server) PutDraftAttachment(ctx context.Context, req PutDraftAttachmentRequestObject) (PutDraftAttachmentResponseObject, error) {
	if _, err := s.companyDraft(ctx, req.Params.XCompanyId, req.DraftId); err != nil {
		return nil, err
	}
	a, err := s.deps.Drafts.StoreAttachment(ctx, claimsFrom(ctx).UserID, req.DraftId, req.AttachmentId, contentTypeFrom(ctx), req.Body)
	if err != nil {
		return nil, s.attachmentFailure("store attachment", err)
	}
	return PutDraftAttachment200JSONResponse(attachmentResponse(a)), nil
}

// DeleteDraftAttachment removes a photo from the user's draft.
func (s *Server) DeleteDraftAttachment(ctx context.Context, req DeleteDraftAttachmentRequestObject) (DeleteDraftAttachmentResponseObject, error) {
	if _, err := s.companyDraft(ctx, req.Params.XCompanyId, req.DraftId); err != nil {
		return nil, err
	}
	if err := s.deps.Drafts.DeleteAttachment(ctx, claimsFrom(ctx).UserID, req.DraftId, req.AttachmentId); err != nil {
		return nil, s.attachmentFailure("delete attachment", err)
	}
	return DeleteDraftAttachment204Response{}, nil
}

// manifestInput converts a create request's manifest for the pipeline,
// checking each entry against the contract.
func manifestInput(items *[]AttachmentManifestItem) ([]pipeline.AttachmentInput, error) {
	if items == nil {
		return nil, nil
	}
	out := make([]pipeline.AttachmentInput, 0, len(*items))
	seen := make(map[uuid.UUID]bool, len(*items))
	for _, it := range *items {
		switch {
		case !it.ContentType.Valid():
			return nil, badRequest("photo content_type must be image/jpeg")
		case it.Filename == "" || utf8.RuneCountInString(it.Filename) > 255:
			return nil, badRequest("photo filename must be 1 to 255 characters")
		case it.Bytes < 1:
			return nil, badRequest("photo bytes must be at least 1")
		case seen[it.Id]:
			return nil, badRequest("photo ids must be unique")
		}
		seen[it.Id] = true
		out = append(out, pipeline.AttachmentInput{
			ID: it.Id, Filename: it.Filename, ContentType: string(it.ContentType), Bytes: it.Bytes,
		})
	}
	return out, nil
}

// attachmentResponse converts a pipeline attachment to the contract shape.
func attachmentResponse(a pipeline.Attachment) Attachment {
	out := Attachment{Id: a.ID, Filename: a.Filename, Bytes: a.Bytes, State: AttachmentState(a.State)}
	if a.ProcoreUploadID != "" {
		id := a.ProcoreUploadID
		out.ProcoreUploadId = &id
	}
	if a.Error != nil {
		out.Error = &Error{Code: a.Error.Code, Message: a.Error.Message}
	}
	return out
}
