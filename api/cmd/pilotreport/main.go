// Command pilotreport prints the weekly pilot summary as Markdown, read from
// the audit trail in the database named by DATABASE_URL. It only reads: its
// connections run with default_transaction_read_only on, and it adds no
// tables.
//
//	pilotreport [--from 2026-11-03T00:00:00Z] [--to 2026-11-10T00:00:00Z]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/monroeresidential/fieldassistant/api/internal/report"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "pilotreport:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	now := time.Now().UTC()
	fs := flag.NewFlagSet("pilotreport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromFlag := fs.String("from", now.AddDate(0, 0, -7).Format(time.RFC3339), "start of the window, RFC 3339, inclusive")
	toFlag := fs.String("to", now.Format(time.RFC3339), "end of the window, RFC 3339, exclusive")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: pilotreport [--from RFC3339] [--to RFC3339]")
		fmt.Fprintln(stderr, "Prints the pilot summary for drafts created in [from, to) as Markdown.")
		fmt.Fprintln(stderr, "Reads DATABASE_URL; read-only. Default window: the last seven days.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	from, err := time.Parse(time.RFC3339, *fromFlag)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	to, err := time.Parse(time.RFC3339, *toFlag)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	if !from.Before(to) {
		return fmt.Errorf("--from %s is not before --to %s", *fromFlag, *toFlag)
	}
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := openReadOnly(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	s, err := report.Build(ctx, db.New(pool), report.Window{From: from, To: to})
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, report.Markdown(s))
	return err
}

// openReadOnly connects with every transaction read-only, so a bug here
// cannot write to the database.
func openReadOnly(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
