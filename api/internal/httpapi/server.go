package httpapi

import "log/slog"

// Server implements StrictServerInterface.
type Server struct {
	deps Deps
}

var _ StrictServerInterface = (*Server)(nil)

// logger returns the configured logger, or slog.Default() when none is set.
func (s *Server) logger() *slog.Logger {
	if s.deps.Logger != nil {
		return s.deps.Logger
	}
	return slog.Default()
}
