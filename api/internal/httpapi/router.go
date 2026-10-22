package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Deps holds the dependencies handlers need. It grows in later tasks.
type Deps struct {
	// Logger receives request and error logs. Nil means slog.Default().
	Logger *slog.Logger
}

// NewRouter builds the HTTP handler for the API.
func NewRouter(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Get("/healthz", handleHealthz)

	strict := NewStrictHandlerWithOptions(&Server{deps: deps}, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequestErrorHandler,
		ResponseErrorHandlerFunc: newStrictErrorHandler(logger),
	})
	r.Group(func(v1 chi.Router) {
		v1.Use(requireBearerStub)
		HandlerWithOptions(strict, ChiServerOptions{
			BaseURL:          "/v1",
			BaseRouter:       v1,
			ErrorHandlerFunc: badRequestErrorHandler,
		})
	})
	return r
}

// publicPaths are the /v1 routes that need no bearer token.
var publicPaths = map[string]bool{
	"/v1/auth/start":    true,
	"/v1/auth/callback": true,
	"/v1/auth/exchange": true,
	"/v1/auth/refresh":  true,
}

// requireBearerStub rejects /v1 requests without an Authorization header,
// except publicPaths. Real session verification replaces it in a later task.
func requireBearerStub(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !publicPaths[r.URL.Path] && r.Header.Get("Authorization") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration", time.Since(start),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
