package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/a-jay98/stockpot/internal/reqctx"
)

// Stable problem codes. Clients branch on these, never on titles or detail.
const (
	CodeBadRequest       = "bad_request"
	CodeUnauthenticated  = "unauthenticated"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeTooLarge         = "payload_too_large"
	CodeCrossOrigin      = "cross_origin_request"
	CodeInternal         = "internal"
	CodeInvalidValue     = "invalid_value"
	CodeNotChanged       = "not_changed"
	// CodeLLMUnavailable: no LLM provider could answer; the client offers
	// the manual path.
	CodeLLMUnavailable = "llm_unavailable"
)

// NewProblem builds an RFC 9457 problem for status. detail is shown to the
// client, so it must never carry internals (SQL, paths, stack traces).
func NewProblem(r *http.Request, status int, code, detail string) Problem {
	return problemCtx(r.Context(), status, code, detail)
}

// problemCtx is NewProblem for strict handlers, which see only the context.
func problemCtx(ctx context.Context, status int, code, detail string) Problem {
	p := Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
	}
	if detail != "" {
		p.Detail = &detail
	}
	if info := reqctx.From(ctx); info != nil && info.RequestID != "" {
		p.Instance = &info.RequestID
	}
	return p
}

// WriteProblem sends p as application/problem+json.
func WriteProblem(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	WriteProblem(w, NewProblem(r, status, code, detail))
}
