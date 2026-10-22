package httpapi

import (
	"io"
	"log/slog"
)

// testDeps returns Deps with a discarding logger so test output stays clean.
func testDeps() Deps {
	return Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
