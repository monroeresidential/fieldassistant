package httpapi

// Server implements StrictServerInterface.
type Server struct {
	deps Deps
}

var _ StrictServerInterface = (*Server)(nil)
