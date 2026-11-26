package httpapi

// Handlers for setup, sign-in, the account, invites and admin user
// management. The logic lives in internal/identity; these map types.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/reqctx"
)

// --- session cookie and middleware ---------------------------------------------

func sessionCookie(tok identity.SessionToken) string {
	c := &http.Cookie{
		Name: identity.SessionCookie, Value: tok.Token, Path: "/",
		Expires: tok.ExpiresAt.UTC(), MaxAge: max(int(time.Until(tok.ExpiresAt).Seconds()), 1),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	return c.String()
}

func clearSessionCookie() string {
	c := &http.Cookie{
		Name: identity.SessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	return c.String()
}

// SessionLoader loads the caller's session from its cookie into the request
// context (identity.PrincipalFrom, reqctx.Info). An invalid cookie is
// cleared and the request goes on anonymous; authorization decides.
func SessionLoader(svc *identity.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(identity.SessionCookie)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			p, tok, err := svc.Authenticate(r.Context(), c.Value)
			switch {
			case errors.Is(err, identity.ErrUnauthenticated):
				w.Header().Add("Set-Cookie", clearSessionCookie())
				next.ServeHTTP(w, r)
				return
			case err != nil:
				writeProblem(w, r, http.StatusInternalServerError, CodeInternal, "")
				return
			}
			if tok.Token != "" {
				// Sliding expiry: keep the cookie's lifetime in step with the row.
				w.Header().Add("Set-Cookie", sessionCookie(tok))
			}
			markRequest(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
		})
	}
}

// markRequest records who is calling, for the access log and audit events.
func markRequest(ctx context.Context, p *identity.Principal) {
	if info := reqctx.From(ctx); info != nil && p != nil {
		info.UserID, info.SessionID, info.HouseholdID = p.UserID, p.SessionID, p.HouseholdID
	}
}

// IdentityAuthorizer enforces x-roles with the identity policy.
type IdentityAuthorizer struct{}

// Authorize implements Authorizer.
func (IdentityAuthorizer) Authorize(r *http.Request, roles []string) error {
	switch err := identity.Allow(identity.PrincipalFrom(r.Context()), roles); {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrUnauthenticated):
		return ErrUnauthenticated
	default:
		return ErrForbidden
	}
}

// --- error mapping ---------------------------------------------------------------

var identityStatus = map[*identity.Error]int{
	identity.ErrUnauthenticated:   http.StatusUnauthorized,
	identity.ErrForbidden:         http.StatusForbidden,
	identity.ErrNotFound:          http.StatusNotFound,
	identity.ErrSetupUnavailable:  http.StatusConflict,
	identity.ErrSetupNotEnabled:   http.StatusServiceUnavailable,
	identity.ErrSetupToken:        http.StatusForbidden,
	identity.ErrCeremonyInvalid:   http.StatusBadRequest,
	identity.ErrCredentialInvalid: http.StatusUnauthorized,
	identity.ErrAccountDisabled:   http.StatusForbidden,
	identity.ErrInviteInvalid:     http.StatusGone,
	identity.ErrLastCredential:    http.StatusConflict,
	identity.ErrLastAdmin:         http.StatusConflict,
	identity.ErrNotPending:        http.StatusConflict,
	identity.ErrUserPending:       http.StatusConflict,
	identity.ErrNoHousehold:       http.StatusConflict,
	identity.ErrAlreadyMember:     http.StatusConflict,
}

// identityProblem maps an identity error to a problem, or reports false
// for an unexpected error (a 500 whose cause stays in the log).
func identityProblem(err error) (status int, code, detail string, ok bool) {
	var ie *identity.Error
	if errors.As(err, &ie) {
		if st, known := identityStatus[ie]; known {
			return st, ie.Code, ie.Msg, true
		}
	}
	var ve *identity.ValidationError
	if errors.As(err, &ve) {
		return http.StatusUnprocessableEntity, "invalid_value", ve.Error(), true
	}
	return 0, "", "", false
}

// --- conversions -----------------------------------------------------------------

func toMe(m identity.Me) Me {
	out := Me{User: toUser(m.UserDetail), Households: toMemberships(m.Households)}
	if m.ActiveHouseholdID != uuid.Nil {
		id := m.ActiveHouseholdID
		out.ActiveHouseholdId = &id
	}
	return out
}

func toUser(u identity.UserDetail) User {
	return User{
		Id: u.ID, DisplayName: u.DisplayName, Status: UserStatus(u.Status),
		SystemRole: SystemRole(u.SystemRole), CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

func toAdminUser(u identity.UserDetail) AdminUser {
	return AdminUser{
		Id: u.ID, DisplayName: u.DisplayName, Status: UserStatus(u.Status),
		SystemRole: SystemRole(u.SystemRole), CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
		ApprovedAt: u.ApprovedAt, ApprovedBy: u.ApprovedBy, Households: toMemberships(u.Households),
	}
}

func toMemberships(ms []identity.Membership) []HouseholdMembership {
	out := make([]HouseholdMembership, len(ms))
	for i, m := range ms {
		out[i] = HouseholdMembership{HouseholdId: m.HouseholdID, Name: m.Name, Role: HouseholdRole(m.Role)}
	}
	return out
}

func toSessions(ss []identity.SessionInfo) []Session {
	out := make([]Session, len(ss))
	for i, s := range ss {
		out[i] = Session{
			Id: s.ID, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt,
			UserAgent: s.UserAgent, Current: s.Current,
		}
		if s.Ip != nil {
			ip := s.Ip.String()
			out[i].Ip = &ip
		}
	}
	return out
}

func toInvite(i identity.Invite) Invite {
	return Invite{
		Id: i.ID, HouseholdId: i.HouseholdID, HouseholdName: i.HouseholdName, Role: HouseholdRole(i.Role),
		Note: i.Note, CreatedBy: i.CreatedBy, CreatedByName: i.CreatedByName, CreatedAt: i.CreatedAt,
		ExpiresAt: i.ExpiresAt, AcceptedAt: i.AcceptedAt, AcceptedBy: i.AcceptedBy, Status: InviteStatus(i.Status),
	}
}

func toCeremony(c identity.CeremonyStart) (CeremonyStart, error) {
	var opts map[string]any
	if err := json.Unmarshal(c.Options, &opts); err != nil {
		return CeremonyStart{}, err
	}
	return CeremonyStart{CeremonyId: c.ID, Options: opts, ExpiresAt: c.ExpiresAt}, nil
}

func credentialJSON(f *CeremonyFinish) (json.RawMessage, string, error) {
	raw, err := json.Marshal(f.Credential)
	if err != nil {
		return nil, "", err
	}
	name := ""
	if f.Name != nil {
		name = *f.Name
	}
	return raw, name, nil
}

// signedIn loads the new session into ctx and returns the account.
func (s *Server) signedIn(ctx context.Context, tok identity.SessionToken) (SignedInJSONResponse, error) {
	p, _, err := s.identity.Authenticate(ctx, tok.Token)
	if err != nil {
		return SignedInJSONResponse{}, err
	}
	me, err := s.identity.Me(identity.WithPrincipal(ctx, p))
	if err != nil {
		return SignedInJSONResponse{}, err
	}
	cookie := sessionCookie(tok)
	return SignedInJSONResponse{Body: toMe(me), Headers: SignedInResponseHeaders{SetCookie: &cookie}}, nil
}

// --- setup and sign-in --------------------------------------------------------------

// GetSetupStatus serves getSetupStatus.
func (s *Server) GetSetupStatus(ctx context.Context, _ GetSetupStatusRequestObject) (GetSetupStatusResponseObject, error) {
	needed, configured, err := s.identity.SetupStatus(ctx)
	if err != nil {
		return nil, err
	}
	return GetSetupStatus200JSONResponse{Needed: needed, TokenConfigured: configured}, nil
}

// BeginSetup serves beginSetup.
func (s *Server) BeginSetup(ctx context.Context, req BeginSetupRequestObject) (BeginSetupResponseObject, error) {
	c, err := s.identity.BeginSetup(ctx, req.Body.Token, req.Body.DisplayName, req.Body.HouseholdName)
	if err != nil {
		return nil, err
	}
	out, err := toCeremony(c)
	return BeginSetup200JSONResponse(out), err
}

// FinishSetup serves finishSetup.
func (s *Server) FinishSetup(ctx context.Context, req FinishSetupRequestObject) (FinishSetupResponseObject, error) {
	cred, name, err := credentialJSON(req.Body)
	if err != nil {
		return nil, err
	}
	tok, err := s.identity.FinishSetup(ctx, req.Body.CeremonyId, cred, name)
	if err != nil {
		return nil, err
	}
	out, err := s.signedIn(ctx, tok)
	return FinishSetup201JSONResponse{out}, err
}

// BeginLogin serves beginLogin.
func (s *Server) BeginLogin(ctx context.Context, _ BeginLoginRequestObject) (BeginLoginResponseObject, error) {
	c, err := s.identity.BeginLogin(ctx)
	if err != nil {
		return nil, err
	}
	out, err := toCeremony(c)
	return BeginLogin200JSONResponse(out), err
}

// FinishLogin serves finishLogin.
func (s *Server) FinishLogin(ctx context.Context, req FinishLoginRequestObject) (FinishLoginResponseObject, error) {
	cred, _, err := credentialJSON(req.Body)
	if err != nil {
		return nil, err
	}
	tok, err := s.identity.FinishLogin(ctx, req.Body.CeremonyId, cred)
	if err != nil {
		return nil, err
	}
	out, err := s.signedIn(ctx, tok)
	return FinishLogin200JSONResponse{out}, err
}

// Logout serves logout.
func (s *Server) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	if err := s.identity.Logout(ctx); err != nil && !errors.Is(err, identity.ErrNotFound) {
		return nil, err
	}
	c := clearSessionCookie()
	return Logout204Response{Headers: Logout204ResponseHeaders{SetCookie: &c}}, nil
}

// --- account ----------------------------------------------------------------------------

// GetMe serves getMe.
func (s *Server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	me, err := s.identity.Me(ctx)
	if err != nil {
		return nil, err
	}
	return GetMe200JSONResponse(toMe(me)), nil
}

// UpdateMe serves updateMe.
func (s *Server) UpdateMe(ctx context.Context, req UpdateMeRequestObject) (UpdateMeResponseObject, error) {
	me, err := s.identity.UpdateMe(ctx, req.Body.DisplayName)
	if err != nil {
		return nil, err
	}
	return UpdateMe200JSONResponse(toMe(me)), nil
}

// SwitchHousehold serves switchHousehold.
func (s *Server) SwitchHousehold(ctx context.Context, req SwitchHouseholdRequestObject) (SwitchHouseholdResponseObject, error) {
	me, err := s.identity.SwitchHousehold(ctx, req.Body.HouseholdId)
	if err != nil {
		return nil, err
	}
	markRequest(ctx, identity.PrincipalFrom(ctx))
	return SwitchHousehold200JSONResponse(toMe(me)), nil
}

// ListMySessions serves listMySessions.
func (s *Server) ListMySessions(ctx context.Context, _ ListMySessionsRequestObject) (ListMySessionsResponseObject, error) {
	ss, err := s.identity.ListMySessions(ctx)
	if err != nil {
		return nil, err
	}
	return ListMySessions200JSONResponse(toSessions(ss)), nil
}

// RevokeMySession serves revokeMySession.
func (s *Server) RevokeMySession(ctx context.Context, req RevokeMySessionRequestObject) (RevokeMySessionResponseObject, error) {
	if err := s.identity.RevokeMySession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	return RevokeMySession204Response{}, nil
}

// ListMyPasskeys serves listMyPasskeys.
func (s *Server) ListMyPasskeys(ctx context.Context, _ ListMyPasskeysRequestObject) (ListMyPasskeysResponseObject, error) {
	cs, err := s.identity.ListMyPasskeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make(ListMyPasskeys200JSONResponse, len(cs))
	for i, c := range cs {
		out[i] = Passkey{Id: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, Synced: c.Synced}
	}
	return out, nil
}

// BeginAddPasskey serves beginAddPasskey.
func (s *Server) BeginAddPasskey(ctx context.Context, _ BeginAddPasskeyRequestObject) (BeginAddPasskeyResponseObject, error) {
	c, err := s.identity.BeginAddPasskey(ctx)
	if err != nil {
		return nil, err
	}
	out, err := toCeremony(c)
	return BeginAddPasskey200JSONResponse(out), err
}

// FinishAddPasskey serves finishAddPasskey.
func (s *Server) FinishAddPasskey(ctx context.Context, req FinishAddPasskeyRequestObject) (FinishAddPasskeyResponseObject, error) {
	cred, name, err := credentialJSON(req.Body)
	if err != nil {
		return nil, err
	}
	if err := s.identity.FinishAddPasskey(ctx, req.Body.CeremonyId, cred, name); err != nil {
		return nil, err
	}
	return FinishAddPasskey204Response{}, nil
}

// RemoveMyPasskey serves removeMyPasskey.
func (s *Server) RemoveMyPasskey(ctx context.Context, req RemoveMyPasskeyRequestObject) (RemoveMyPasskeyResponseObject, error) {
	if err := s.identity.RemoveMyPasskey(ctx, req.PasskeyId); err != nil {
		return nil, err
	}
	return RemoveMyPasskey204Response{}, nil
}

// --- invites ------------------------------------------------------------------------------

// ListInvites serves listInvites.
func (s *Server) ListInvites(ctx context.Context, req ListInvitesRequestObject) (ListInvitesResponseObject, error) {
	hh := uuid.Nil
	if req.Params.HouseholdId != nil {
		hh = *req.Params.HouseholdId
	}
	all := req.Params.All != nil && *req.Params.All
	invs, err := s.identity.ListInvites(ctx, hh, all)
	if err != nil {
		return nil, err
	}
	out := make(ListInvites200JSONResponse, len(invs))
	for i, inv := range invs {
		out[i] = toInvite(inv)
	}
	return out, nil
}

// CreateInvite serves createInvite.
func (s *Server) CreateInvite(ctx context.Context, req CreateInviteRequestObject) (CreateInviteResponseObject, error) {
	hh := uuid.Nil
	if req.Body.HouseholdId != nil {
		hh = *req.Body.HouseholdId
	}
	note := ""
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	inv, err := s.identity.CreateInvite(ctx, hh, string(req.Body.Role), note)
	if err != nil {
		return nil, err
	}
	return CreateInvite201JSONResponse{Invite: toInvite(inv.Invite), Code: inv.Code, Url: inv.URL}, nil
}

// RevokeInvite serves revokeInvite.
func (s *Server) RevokeInvite(ctx context.Context, req RevokeInviteRequestObject) (RevokeInviteResponseObject, error) {
	if err := s.identity.RevokeInvite(ctx, req.InviteId); err != nil {
		return nil, err
	}
	return RevokeInvite204Response{}, nil
}

// InspectInvite serves inspectInvite.
func (s *Server) InspectInvite(ctx context.Context, req InspectInviteRequestObject) (InspectInviteResponseObject, error) {
	info, err := s.identity.InspectInvite(ctx, req.Body.Code)
	if err != nil {
		return nil, err
	}
	return InspectInvite200JSONResponse{HouseholdName: info.HouseholdName, Role: HouseholdRole(info.Role), ExpiresAt: info.ExpiresAt}, nil
}

// BeginAcceptInvite serves beginAcceptInvite.
func (s *Server) BeginAcceptInvite(ctx context.Context, req BeginAcceptInviteRequestObject) (BeginAcceptInviteResponseObject, error) {
	c, err := s.identity.BeginAcceptInvite(ctx, req.Body.Code, req.Body.DisplayName)
	if err != nil {
		return nil, err
	}
	out, err := toCeremony(c)
	return BeginAcceptInvite200JSONResponse(out), err
}

// FinishAcceptInvite serves finishAcceptInvite.
func (s *Server) FinishAcceptInvite(ctx context.Context, req FinishAcceptInviteRequestObject) (FinishAcceptInviteResponseObject, error) {
	cred, name, err := credentialJSON(req.Body)
	if err != nil {
		return nil, err
	}
	tok, err := s.identity.FinishAcceptInvite(ctx, req.Body.CeremonyId, cred, name)
	if err != nil {
		return nil, err
	}
	out, err := s.signedIn(ctx, tok)
	return FinishAcceptInvite201JSONResponse{out}, err
}

// JoinWithInvite serves joinWithInvite.
func (s *Server) JoinWithInvite(ctx context.Context, req JoinWithInviteRequestObject) (JoinWithInviteResponseObject, error) {
	me, err := s.identity.JoinWithInvite(ctx, req.Body.Code)
	if err != nil {
		return nil, err
	}
	return JoinWithInvite200JSONResponse(toMe(me)), nil
}

// --- admin: users ----------------------------------------------------------------------------

// AdminListUsers serves adminListUsers.
func (s *Server) AdminListUsers(ctx context.Context, req AdminListUsersRequestObject) (AdminListUsersResponseObject, error) {
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	us, err := s.identity.AdminListUsers(ctx, status)
	if err != nil {
		return nil, err
	}
	out := make(AdminListUsers200JSONResponse, len(us))
	for i, u := range us {
		out[i] = toAdminUser(u)
	}
	return out, nil
}

// AdminUpdateUser serves adminUpdateUser.
func (s *Server) AdminUpdateUser(ctx context.Context, req AdminUpdateUserRequestObject) (AdminUpdateUserResponseObject, error) {
	var up identity.UserUpdate
	if req.Body.Status != nil {
		st := string(*req.Body.Status)
		up.Status = &st
	}
	if req.Body.SystemRole != nil {
		r := string(*req.Body.SystemRole)
		up.SystemRole = &r
	}
	u, err := s.identity.AdminUpdateUser(ctx, req.UserId, up)
	if err != nil {
		return nil, err
	}
	return AdminUpdateUser200JSONResponse(toAdminUser(u)), nil
}

// AdminApproveUser serves adminApproveUser.
func (s *Server) AdminApproveUser(ctx context.Context, req AdminApproveUserRequestObject) (AdminApproveUserResponseObject, error) {
	u, err := s.identity.ApproveUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return AdminApproveUser200JSONResponse(toAdminUser(u)), nil
}

// AdminRejectUser serves adminRejectUser.
func (s *Server) AdminRejectUser(ctx context.Context, req AdminRejectUserRequestObject) (AdminRejectUserResponseObject, error) {
	u, err := s.identity.RejectUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return AdminRejectUser200JSONResponse(toAdminUser(u)), nil
}

// AdminListUserSessions serves adminListUserSessions.
func (s *Server) AdminListUserSessions(ctx context.Context, req AdminListUserSessionsRequestObject) (AdminListUserSessionsResponseObject, error) {
	ss, err := s.identity.AdminListSessions(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return AdminListUserSessions200JSONResponse(toSessions(ss)), nil
}

// AdminRevokeSession serves adminRevokeSession.
func (s *Server) AdminRevokeSession(ctx context.Context, req AdminRevokeSessionRequestObject) (AdminRevokeSessionResponseObject, error) {
	if err := s.identity.AdminRevokeSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	return AdminRevokeSession204Response{}, nil
}

// AdminListHouseholds serves adminListHouseholds.
func (s *Server) AdminListHouseholds(ctx context.Context, _ AdminListHouseholdsRequestObject) (AdminListHouseholdsResponseObject, error) {
	hs, err := s.identity.AdminListHouseholds(ctx)
	if err != nil {
		return nil, err
	}
	out := make(AdminListHouseholds200JSONResponse, len(hs))
	for i, h := range hs {
		out[i] = Household{Id: h.ID, Name: h.Name, CreatedAt: h.CreatedAt, MemberCount: int(h.MemberCount)}
	}
	return out, nil
}
