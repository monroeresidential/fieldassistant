package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/features/rfi"
	"github.com/monroeresidential/fieldassistant/api/internal/httpapi"
	"github.com/monroeresidential/fieldassistant/api/internal/jobs"
	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/records"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// procoreTimeout bounds every outbound request to Procore.
const procoreTimeout = 30 * time.Second

// lookupCacheTTL is how long a project's cached Procore lookup lists are
// trusted before they are fetched again.
const lookupCacheTTL = 10 * time.Minute

// companyCacheTTL is how long a user's Procore company list is trusted for
// scope checks before it is fetched again.
const companyCacheTTL = 10 * time.Minute

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	deps, closeDeps, err := buildDeps(ctx, cfg)
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer closeDeps()

	// Jobs start before the server accepts requests and stop before the
	// database pool closes (closeDeps is deferred above, so it runs last).
	// River gets a context the signal never cancels: cancelling the Start
	// context is a hard stop that skips the graceful drain. Shutdown takes
	// at most 25 s (HTTP 10 s, jobs 10 s, cancel 5 s), inside the 30 s
	// stop_grace_period in deploy/docker-compose.yml. A job killed anyway
	// is retried after jobs.RescueStuckJobsAfter.
	if err := deps.Jobs.Start(context.Background()); err != nil {
		slog.Error("starting jobs failed", "error", err)
		closeDeps()
		os.Exit(1)
	}
	slog.Info("jobs started")
	stopJobs := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := deps.Jobs.Stop(stopCtx); err != nil {
			slog.Warn("jobs did not drain in time, cancelling", "error", err)
			cancelCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelStop()
			if err := deps.Jobs.StopAndCancel(cancelCtx); err != nil {
				slog.Error("stopping jobs failed", "error", err)
			}
		}
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			stopJobs()
			closeDeps()
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown failed", "error", err)
			stopJobs()
			closeDeps()
			os.Exit(1)
		}
		stopJobs()
	}
}

// buildDeps opens and migrates the database and constructs every handler
// dependency. The returned func releases the database pool.
func buildDeps(ctx context.Context, cfg config.Config) (httpapi.Deps, func(), error) {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return httpapi.Deps{}, nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return httpapi.Deps{}, nil, err
	}
	vault, err := auth.NewVault(cfg.TokenVaultKey)
	if err != nil {
		st.Close()
		return httpapi.Deps{}, nil, err
	}

	hc := &http.Client{Timeout: procoreTimeout}
	oauth := procore.NewOAuth(cfg.Procore, hc, time.Now)
	pc := procore.NewClient(cfg.Procore.APIURL, hc, nil, nil)
	tokens := auth.NewTokenSource(st, vault, oauth, time.Now)
	sessions := auth.NewSessions(st, cfg.SessionSecret, time.Now)

	logger := slog.Default()
	blobs, err := buildBlobs(ctx, cfg.ObjectStore)
	if err != nil {
		st.Close()
		return httpapi.Deps{}, nil, err
	}
	extractor := llm.NewClient(cfg.LLM.APIKey, cfg.LLM.ExtractModel, "", cfg.LLM.Timeout)
	lookups := pipeline.NewLookups(st, pc, tokens, lookupCacheTTL, time.Now, logger)
	features := pipeline.NewFeatures(lookups, pc, tokens, pipeline.FeatureProbe(cfg.FeaturesProbe), lookupCacheTTL, time.Now, logger)
	drafts := pipeline.NewDrafts(st, lookups, extractor, pc, tokens, blobs,
		map[string]pipeline.Feature{"rfi": rfi.New()}, cfg.LLM.ExtractModel, cfg.LLM.Timeout, time.Now, logger)
	drafts.SetFeatureStates(features)
	mirror := records.NewMirror(st, map[records.Kind]records.Fetcher{
		records.KindRFI: records.RFIFetcher{PC: pc},
	}, tokens, time.Now, logger)
	jobsClient, err := jobs.NewClient(st.Pool, drafts, mirror, logger)
	if err != nil {
		st.Close()
		return httpapi.Deps{}, nil, err
	}
	return httpapi.Deps{
		Logger:    logger,
		Config:    cfg,
		Store:     st,
		Sessions:  sessions,
		Broker:    auth.NewBroker(st, oauth, pc, tokens, sessions, time.Now),
		Tokens:    tokens,
		Procore:   pc,
		Companies: auth.NewCompanyCache(pc, tokens, companyCacheTTL, time.Now),
		Lookups:   lookups,
		Features:  features,
		Drafts:    drafts,
		Jobs:      jobsClient,
		Records:   mirror,
	}, st.Close, nil
}

// buildBlobs builds the attachment object store and, when configured (local
// development), creates its bucket.
func buildBlobs(ctx context.Context, oc config.ObjectStoreConfig) (*blob.S3, error) {
	s, err := blob.NewS3(blob.S3Config{
		Endpoint:  oc.Endpoint,
		Region:    oc.Region,
		Bucket:    oc.Bucket,
		AccessKey: oc.AccessKey,
		SecretKey: oc.SecretKey,
		PathStyle: true, // Spaces and MinIO both accept path-style addressing.
	})
	if err != nil {
		return nil, err
	}
	if oc.CreateBucket {
		if err := s.EnsureBucket(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}
