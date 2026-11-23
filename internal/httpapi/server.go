package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/a-jay98/stockpot/internal/buildinfo"
)

// Pinger is a dependency the health check probes, e.g. the database pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server implements StrictServerInterface.
type Server struct {
	checks map[string]Pinger
}

var _ StrictServerInterface = (*Server)(nil)

// NewServer returns the API implementation. checks are probed by /api/health,
// keyed by the name reported to the client.
func NewServer(checks map[string]Pinger) *Server {
	return &Server{checks: checks}
}

// healthTimeout bounds each dependency probe; the deploy step polls often.
const healthTimeout = 2 * time.Second

// GetHealth reports the build and whether every dependency answers.
func (s *Server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	bi := buildinfo.Get()
	h := Health{
		Status:  Ok,
		Version: bi.Version,
		Commit:  bi.Commit,
		BuiltAt: bi.BuiltAt,
		Checks:  make(map[string]string, len(s.checks)),
	}
	for name, p := range s.checks {
		pctx, cancel := context.WithTimeout(ctx, healthTimeout)
		err := p.Ping(pctx)
		cancel()
		if err != nil {
			// /api/health is public; the cause (paths, roles, SQL) stays in the log.
			slog.WarnContext(ctx, "health check failed", "check", name, "err", err)
			h.Status = Degraded
			h.Checks[name] = "unavailable"
			continue
		}
		h.Checks[name] = "ok"
	}
	if h.Status != Ok {
		return GetHealth503JSONResponse(h), nil
	}
	return GetHealth200JSONResponse(h), nil
}
