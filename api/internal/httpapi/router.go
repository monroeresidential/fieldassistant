package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// Deps holds the dependencies handlers need.
type Deps struct {
	// Logger receives request and error logs. Nil means slog.Default().
	Logger   *slog.Logger
	Config   config.Config
	Store    *store.Store
	Sessions *auth.Sessions
	Broker   *auth.Broker
	Tokens   *auth.TokenSource
	Procore  *procore.Client
	// Companies caches each user's Procore company list for scope checks.
	Companies *auth.CompanyCache
	// Lookups serves project lookup lists from the Postgres-backed cache.
	Lookups *pipeline.Lookups
	// Drafts extracts, stores, revalidates and submits drafts.
	Drafts *pipeline.Drafts
	// Jobs is the River client that queues submit jobs and reports on them.
	Jobs *river.Client[pgx.Tx]
	// Records is the per-user Procore record mirror behind the records
	// delta feed.
	Records *records.Mirror
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
		v1.Use(requireSession(deps.Sessions, logger))
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
