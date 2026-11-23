package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// Roles an operation may list in x-roles.
const (
	RolePublic = "public" // no session needed
	RoleUser   = "user"   // any approved, signed-in user
	RoleAdmin  = "admin"  // system administrator
)

var knownRoles = []string{RolePublic, RoleUser, RoleAdmin}

// Authorization errors an Authorizer returns.
var (
	ErrUnauthenticated = errors.New("sign in required")
	ErrForbidden       = errors.New("not allowed")
)

// Authorizer decides whether the request may call an operation that
// allows roles. It returns nil, ErrUnauthenticated or ErrForbidden.
type Authorizer interface {
	Authorize(r *http.Request, roles []string) error
}

// PublicOnly admits only operations marked public. It is the policy until
// identity provides sessions: everything else answers 401.
type PublicOnly struct{}

// Authorize implements Authorizer.
func (PublicOnly) Authorize(_ *http.Request, roles []string) error {
	if slices.Contains(roles, RolePublic) {
		return nil
	}
	return ErrUnauthenticated
}

// RouteRoles maps each operation id to the roles its x-roles extension lists.
type RouteRoles map[string][]string

// ParseRoles reads x-roles from every operation. A missing, empty or unknown
// role list is an error, so no route can ship without a deliberate policy.
func ParseRoles(spec *openapi3.T) (RouteRoles, error) {
	out := RouteRoles{}
	var errs []error
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			where := fmt.Sprintf("%s %s", method, path)
			if op.OperationID == "" {
				errs = append(errs, fmt.Errorf("%s: missing operationId", where))
				continue
			}
			roles, err := rolesOf(op)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", where, err))
				continue
			}
			out[op.OperationID] = roles
		}
	}
	return out, errors.Join(errs...)
}

func rolesOf(op *openapi3.Operation) ([]string, error) {
	raw, ok := op.Extensions["x-roles"]
	if !ok {
		return nil, errors.New("missing x-roles")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("x-roles must be a non-empty list")
	}
	roles := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok || !slices.Contains(knownRoles, s) {
			return nil, fmt.Errorf("x-roles: unknown role %v (want one of %v)", v, knownRoles)
		}
		roles = append(roles, s)
	}
	return roles, nil
}

// authorize finds the request's operation and asks az about its roles. A
// request matching no operation is answered here (404/405) rather than
// passed on, so a disagreement between this router and chi can never skip
// authorization.
func authorize(spec *openapi3.T, roles RouteRoles, az Authorizer) (func(http.Handler) http.Handler, error) {
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		return nil, fmt.Errorf("openapi router: %w", err)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, _, err := router.FindRoute(r)
			if err != nil {
				switch {
				case errors.Is(err, routers.ErrMethodNotAllowed):
					writeProblem(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "")
				default:
					writeProblem(w, r, http.StatusNotFound, CodeNotFound, "")
				}
				return
			}
			switch err := az.Authorize(r, roles[route.Operation.OperationID]); {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, ErrUnauthenticated):
				writeProblem(w, r, http.StatusUnauthorized, CodeUnauthenticated, "Sign in to continue.")
			default:
				writeProblem(w, r, http.StatusForbidden, CodeForbidden, "")
			}
		})
	}, nil
}
