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
	"github.com/monroeresidential/fieldassistant/api/internal/config"
	"github.com/monroeresidential/fieldassistant/api/internal/httpapi"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
)

// procoreTimeout bounds every outbound request to Procore.
const procoreTimeout = 30 * time.Second

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
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown failed", "error", err)
			os.Exit(1)
		}
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
	return httpapi.Deps{
		Config:   cfg,
		Store:    st,
		Sessions: sessions,
		Broker:   auth.NewBroker(st, oauth, pc, tokens, sessions, time.Now),
		Tokens:   tokens,
		Procore:  pc,
	}, st.Close, nil
}
