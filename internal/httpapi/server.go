package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/a-jay98/stockpot/internal/admin"
	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/identity"
)

// Pinger is a dependency the health check probes, e.g. the database pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the services the API is built on. Each feature adds its own.
type Deps struct {
	// Checks are probed by /api/health, keyed by the name reported to the client.
	Checks map[string]Pinger
	// Admin serves /api/v1/admin/*.
	Admin *admin.Service
	// Identity serves setup, sign-in, the account, invites and admin users.
	Identity *identity.Service
	// LLM is the LLM gateway (/api/v1/admin/llm/*); nil answers its
	// endpoints with 503 llm_unavailable.
	LLM LLMGateway
}

// Server implements StrictServerInterface. Handlers live in one file per
// feature area (admin.go, identity.go, …).
type Server struct {
	checks   map[string]Pinger
	admin    *admin.Service
	identity *identity.Service
	llm      LLMGateway
	now      func() time.Time
}

var _ StrictServerInterface = (*Server)(nil)

// NewServer returns the API implementation.
func NewServer(d Deps) *Server {
	return &Server{checks: d.Checks, admin: d.Admin, identity: d.Identity, llm: d.LLM, now: time.Now}
}

// healthTimeout bounds each dependency probe; the deploy step polls often.
const healthTimeout = 2 * time.Second

// GetHealth reports the build and whether every dependency answers.
func (s *Server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	bi := buildinfo.Get()
	h := Health{
		Status:  HealthStatusOk,
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
			h.Status = HealthStatusDegraded
			h.Checks[name] = "unavailable"
			continue
		}
		h.Checks[name] = "ok"
	}
	if h.Status != HealthStatusOk {
		return GetHealth503JSONResponse(h), nil
	}
	return GetHealth200JSONResponse(h), nil
}

// GetMeta reports the contract version, build and server clock.
func (s *Server) GetMeta(context.Context, GetMetaRequestObject) (GetMetaResponseObject, error) {
	bi := buildinfo.Get()
	version, err := contractVersion()
	if err != nil {
		return nil, err
	}
	return GetMeta200JSONResponse(Meta{
		Version:    version,
		Tag:        bi.Version,
		Commit:     bi.Commit,
		ServerTime: s.now().UTC(),
	}), nil
}

// contractVersion is info.version of the embedded spec, decoded once.
var contractVersion = sync.OnceValues(func() (string, error) {
	spec, err := GetSpec()
	if err != nil {
		return "", err
	}
	return spec.Info.Version, nil
})
