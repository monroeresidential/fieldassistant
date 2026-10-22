package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// APIError is returned by handlers to produce a specific error response.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

var errNotImplemented = errors.New("not implemented")

// writeError writes an Error JSON body with the given status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: msg})
}

// newStrictErrorHandler maps handler errors to Error responses.
func newStrictErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
			return
		}
		logger.Error("handler error", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

// badRequestErrorHandler maps request body and parameter errors to 400 Error responses.
func badRequestErrorHandler(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
}
