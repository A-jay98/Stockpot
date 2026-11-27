package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/syncdb"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/identity/passkey"
	"github.com/a-jay98/stockpot/internal/identity/passkey/passkeytest"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

const (
	identityOrigin = "https://stockpot.test"
	setupToken     = "let-me-in-0123456789"
)

type env struct {
	t     *testing.T
	h     http.Handler
	pool  *pgxpool.Pool
	store *settings.Store
	svc   *identity.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse(identityOrigin)
	pk, err := passkey.New(pool, origin)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.NewService(identity.Deps{Pool: pool, Settings: store, Enroller: pk, SetupToken: setupToken, PublicURL: origin})
	if err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	catalog.Register(reg, catalog.ExactResolver{})
	syncStore, err := syncdb.New(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, NewServer(Deps{Identity: svc, Commands: commands.NewService(pool, reg), Sync: syncStore}), Options{
		PublicURL: origin, Session: SessionLoader(svc), Authorizer: IdentityAuthorizer{},
	})
	return &env{t: t, h: h, pool: pool, store: store, svc: svc}
}

// client is one browser: a cookie and a passkey authenticator.
type client struct {
	e      *env
	cookie string
	auth   *passkeytest.Authenticator
}

func (e *env) client() *client { return &client{e: e, auth: passkeytest.New(identityOrigin)} }

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.e.t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.e.t.Fatal(err)
		}
		r = httptest.NewRequestWithContext(c.e.t.Context(), method, "/api"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequestWithContext(c.e.t.Context(), method, "/api"+path, nil)
	}
	r.Header.Set("Origin", identityOrigin)
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: identity.SessionCookie, Value: c.cookie})
	}
	rec := do(c.e.h, r)
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == identity.SessionCookie {
			if sc.MaxAge < 0 || sc.Value == "" {
				c.cookie = ""
			} else {
				c.cookie = sc.Value
			}
		}
	}
	return rec
}

// ok calls and decodes a 2xx answer into out (may be nil).
func (c *client) ok(method, path string, body, out any) {
	c.e.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code < 200 || rec.Code > 299 {
		c.e.t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			c.e.t.Fatal(err)
		}
	}
}

// fail expects a problem with status and code.
func (c *client) fail(method, path string, body any, status int, code string) {
	c.e.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code != status {
		c.e.t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, status, rec.Body)
	}
	if p := decodeProblem(c.e.t, rec); p.Code != code {
		c.e.t.Fatalf("%s %s code = %q, want %q", method, path, p.Code, code)
	}
}

func (c *client) enroll(beginPath string, beginBody any, finishPath string) Me {
	c.e.t.Helper()
	var start CeremonyStart
	c.ok(http.MethodPost, beginPath, beginBody, &start)
	cred := c.create(start)
	var me Me
	c.ok(http.MethodPost, finishPath, map[string]any{"ceremonyId": start.CeremonyId, "credential": cred, "name": "Test key"}, &me)
	return me
}

func (c *client) create(start CeremonyStart) json.RawMessage {
	c.e.t.Helper()
	opts, _ := json.Marshal(start.Options)
	cred, err := c.auth.Create(opts)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return cred
}

func (c *client) login() *httptest.ResponseRecorder {
	c.e.t.Helper()
	var start CeremonyStart
	c.ok(http.MethodPost, "/v1/auth/login/begin", nil, &start)
	opts, _ := json.Marshal(start.Options)
	cred, err := c.auth.Get(opts)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return c.do(http.MethodPost, "/v1/auth/login/finish", map[string]any{"ceremonyId": start.CeremonyId, "credential": cred})
}

func (e *env) setupAdmin() (*client, Me) {
	admin := e.client()
	me := admin.enroll("/v1/setup/begin", map[string]any{"token": setupToken, "displayName": "AJ", "householdName": "Home"}, "/v1/setup/finish")
	return admin, me
}

func (e *env) auditCount(action string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_event WHERE action = $1", action).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) invite(c *client, role string) InviteCreated {
	e.t.Helper()
	var inv InviteCreated
	c.ok(http.MethodPost, "/v1/invites", map[string]any{"role": role, "note": "for the test"}, &inv)
	return inv
}

func TestSetupLoginAndLogout(t *testing.T) {
	e := newEnv(t)
	anon := e.client()

	var st SetupStatus
	anon.ok(http.MethodGet, "/v1/setup", nil, &st)
	if !st.Needed || !st.TokenConfigured {
		t.Fatalf("setup status %+v", st)
	}
	anon.fail(http.MethodPost, "/v1/setup/begin", map[string]any{"token": "wrong", "displayName": "AJ", "householdName": "Home"}, http.StatusForbidden, "setup_token_invalid")
	if e.auditCount("auth.setup.token_rejected") != 1 {
		t.Fatal("rejected setup token not audited")
	}

	admin, me := e.setupAdmin()
	if me.User.Status != UserStatusActive || me.User.SystemRole != SystemRoleAdmin || len(me.Households) != 1 ||
		me.Households[0].Role != HouseholdRoleOwner || me.ActiveHouseholdId == nil || *me.ActiveHouseholdId != me.Households[0].HouseholdId {
		t.Fatalf("after setup: %+v", me)
	}
	if admin.cookie == "" {
		t.Fatal("setup set no session cookie")
	}
	anon.ok(http.MethodGet, "/v1/setup", nil, &st)
	if st.Needed {
		t.Fatal("setup still needed")
	}
	// Setup is one-shot, even with the right token.
	e.client().fail(http.MethodPost, "/v1/setup/begin", map[string]any{"token": setupToken, "displayName": "X", "householdName": "Y"}, http.StatusConflict, "setup_unavailable")

	// The session works, then logout ends it.
	admin.ok(http.MethodGet, "/v1/me", nil, &me)
	admin.ok(http.MethodPost, "/v1/auth/logout", nil, nil)
	if admin.cookie != "" {
		t.Fatal("logout did not clear the cookie")
	}
	admin.fail(http.MethodGet, "/v1/me", nil, http.StatusUnauthorized, "unauthenticated")

	// Signing back in with the passkey.
	if rec := admin.login(); rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body)
	}
	admin.ok(http.MethodGet, "/v1/me", nil, &me)
	for action, want := range map[string]int{"auth.setup.complete": 1, "auth.passkey.add": 1, "auth.login": 1, "auth.logout": 1} {
		if got := e.auditCount(action); got != want {
			t.Errorf("audit %s: %d rows, want %d", action, got, want)
		}
	}
}

func TestLoginRejections(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()

	// A ceremony is single use.
	var start CeremonyStart
	admin.ok(http.MethodPost, "/v1/auth/login/begin", nil, &start)
	opts, _ := json.Marshal(start.Options)
	cred, _ := admin.auth.Get(opts)
	body := map[string]any{"ceremonyId": start.CeremonyId, "credential": cred}
	admin.ok(http.MethodPost, "/v1/auth/login/finish", body, nil)
	admin.fail(http.MethodPost, "/v1/auth/login/finish", body, http.StatusBadRequest, "ceremony_expired")

	// A passkey this server never saw.
	stranger := e.client()
	_, _ = stranger.auth.Create(mustJSON(t, map[string]any{"challenge": "AAAA", "rp": map[string]any{"id": "stockpot.test"}, "user": map[string]any{"id": "c3RyYW5nZXI"}}))
	if rec := stranger.login(); rec.Code != http.StatusUnauthorized || decodeProblem(t, rec).Code != "passkey_rejected" {
		t.Fatalf("unknown passkey: %d %s", rec.Code, rec.Body)
	}

	// No user verification.
	admin.auth.SkipUV = true
	if rec := admin.login(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login without UV = %d", rec.Code)
	}
	if e.auditCount("auth.login.failed") != 2 {
		t.Fatalf("failed logins audited %d times", e.auditCount("auth.login.failed"))
	}
}

func TestInviteApprovalFlow(t *testing.T) {
	e := newEnv(t)
	admin, adminMe := e.setupAdmin()
	inv := e.invite(admin, "member")
	if !strings.HasPrefix(inv.Url, identityOrigin+"/invite#") || inv.Invite.Status != InviteStatusPending {
		t.Fatalf("invite %+v", inv)
	}

	guest := e.client()
	var info InviteInfo
	guest.ok(http.MethodPost, "/v1/invites/inspect", map[string]any{"code": inv.Code}, &info)
	if info.HouseholdName != "Home" || info.Role != HouseholdRoleMember {
		t.Fatalf("invite info %+v", info)
	}
	me := guest.enroll("/v1/invites/accept/begin", map[string]any{"code": inv.Code, "displayName": "Sam"}, "/v1/invites/accept/finish")
	if me.User.Status != UserStatusPending {
		t.Fatalf("invited user status %s", me.User.Status)
	}
	// Single use.
	e.client().fail(http.MethodPost, "/v1/invites/inspect", map[string]any{"code": inv.Code}, http.StatusGone, "invite_invalid")

	// Pending: may see their account and sign out, nothing else.
	guest.ok(http.MethodGet, "/v1/me", nil, nil)
	guest.ok(http.MethodGet, "/v1/me/sessions", nil, nil)
	guest.fail(http.MethodGet, "/v1/me/passkeys", nil, http.StatusForbidden, "forbidden")
	guest.fail(http.MethodPost, "/v1/invites", map[string]any{"role": "member"}, http.StatusForbidden, "forbidden")

	var pending []AdminUser
	admin.ok(http.MethodGet, "/v1/admin/users?status=pending", nil, &pending)
	if len(pending) != 1 || pending[0].DisplayName != "Sam" || len(pending[0].Households) != 1 {
		t.Fatalf("pending users %+v", pending)
	}
	var approved AdminUser
	admin.ok(http.MethodPost, "/v1/admin/users/"+me.User.Id.String()+"/approve", nil, &approved)
	if approved.Status != UserStatusActive || approved.ApprovedBy == nil || *approved.ApprovedBy != adminMe.User.Id {
		t.Fatalf("approved %+v", approved)
	}
	admin.fail(http.MethodPost, "/v1/admin/users/"+me.User.Id.String()+"/approve", nil, http.StatusConflict, "not_pending")

	// Approved: the same session now works as a user.
	guest.ok(http.MethodGet, "/v1/me/passkeys", nil, nil)
	// Members can't invite; owners can.
	guest.fail(http.MethodPost, "/v1/invites", map[string]any{"role": "member"}, http.StatusForbidden, "forbidden")

	// Disabling signs them out everywhere.
	admin.ok(http.MethodPatch, "/v1/admin/users/"+me.User.Id.String(), map[string]any{"status": "disabled"}, nil)
	guest.fail(http.MethodGet, "/v1/me", nil, http.StatusUnauthorized, "unauthenticated")
	if rec := guest.login(); rec.Code != http.StatusForbidden || decodeProblem(t, rec).Code != "account_disabled" {
		t.Fatalf("disabled login: %d %s", rec.Code, rec.Body)
	}
	for _, a := range []string{"invite.create", "invite.accept", "user.approve", "user.disable", "auth.login.denied"} {
		if e.auditCount(a) != 1 {
			t.Errorf("audit %s: %d rows", a, e.auditCount(a))
		}
	}
}

func TestInviteWithoutApprovalAndReject(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()

	// Rejected users are disabled and signed out.
	inv := e.invite(admin, "member")
	guest := e.client()
	me := guest.enroll("/v1/invites/accept/begin", map[string]any{"code": inv.Code, "displayName": "Pat"}, "/v1/invites/accept/finish")
	admin.ok(http.MethodPost, "/v1/admin/users/"+me.User.Id.String()+"/reject", nil, nil)
	guest.fail(http.MethodGet, "/v1/me", nil, http.StatusUnauthorized, "unauthenticated")

	if err := settings.SetValue(e.t.Context(), e.store, identity.InviteRequiresApproval, false, nil); err != nil {
		t.Fatal(err)
	}
	inv = e.invite(admin, "owner")
	owner := e.client()
	me = owner.enroll("/v1/invites/accept/begin", map[string]any{"code": inv.Code, "displayName": "Kim"}, "/v1/invites/accept/finish")
	if me.User.Status != UserStatusActive || me.Households[0].Role != HouseholdRoleOwner {
		t.Fatalf("auto-approved owner %+v", me)
	}
	// Owners invite to their household and see its invites.
	e.invite(owner, "member")
	var invs []Invite
	owner.ok(http.MethodGet, "/v1/invites", nil, &invs)
	if len(invs) != 3 {
		t.Fatalf("owner sees %d invites, want 3", len(invs))
	}
	owner.fail(http.MethodGet, "/v1/invites?all=true", nil, http.StatusForbidden, "forbidden")
	owner.ok(http.MethodDelete, "/v1/invites/"+invs[0].Id.String(), nil, nil)
	owner.fail(http.MethodDelete, "/v1/invites/"+invs[0].Id.String(), nil, http.StatusGone, "invite_invalid")
}

func TestHouseholdsAndJoin(t *testing.T) {
	e := newEnv(t)
	admin, adminMe := e.setupAdmin()
	home := *adminMe.ActiveHouseholdId

	// A second household, owned by a new user.
	if err := settings.SetValue(e.t.Context(), e.store, identity.InviteRequiresApproval, false, nil); err != nil {
		t.Fatal(err)
	}
	other := e.client()
	other.enroll("/v1/invites/accept/begin", map[string]any{"code": e.invite(admin, "member").Code, "displayName": "Lee"}, "/v1/invites/accept/finish")
	cabinID := e.createHousehold(t, "Cabin")

	// Admins may invite to any household.
	var inv InviteCreated
	admin.ok(http.MethodPost, "/v1/invites", map[string]any{"role": "member", "householdId": cabinID}, &inv)
	var me Me
	other.ok(http.MethodPost, "/v1/invites/join", map[string]any{"code": inv.Code}, &me)
	if len(me.Households) != 2 || *me.ActiveHouseholdId != cabinID {
		t.Fatalf("after join %+v", me)
	}
	other.ok(http.MethodPut, "/v1/me/household", map[string]any{"householdId": home}, &me)
	if *me.ActiveHouseholdId != home {
		t.Fatal("switch did not stick")
	}
	other.ok(http.MethodGet, "/v1/me", nil, &me)
	if *me.ActiveHouseholdId != home {
		t.Fatal("active household not kept by the session")
	}
	// Not a member: 404.
	admin.fail(http.MethodPut, "/v1/me/household", map[string]any{"householdId": cabinID}, http.StatusNotFound, "not_found")

	var hs []Household
	admin.ok(http.MethodGet, "/v1/admin/households", nil, &hs)
	if len(hs) != 2 {
		t.Fatalf("households %+v", hs)
	}
}

func (e *env) createHousehold(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), "INSERT INTO household (id, name) VALUES (gen_random_uuid(), $1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPasskeysAndSessions(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()

	var keys []Passkey
	admin.ok(http.MethodGet, "/v1/me/passkeys", nil, &keys)
	if len(keys) != 1 || !keys[0].Synced {
		t.Fatalf("passkeys %+v", keys)
	}
	admin.fail(http.MethodDelete, "/v1/me/passkeys/"+keys[0].Id, nil, http.StatusConflict, "last_passkey")

	// Add a second passkey, then remove the first.
	var start CeremonyStart
	admin.ok(http.MethodPost, "/v1/me/passkeys/begin", nil, &start)
	if ex, _ := start.Options["excludeCredentials"].([]any); len(ex) != 1 {
		t.Fatalf("existing passkey not excluded: %v", start.Options["excludeCredentials"])
	}
	admin.ok(http.MethodPost, "/v1/me/passkeys/finish", map[string]any{"ceremonyId": start.CeremonyId, "credential": admin.create(start), "name": "Laptop"}, nil)
	admin.ok(http.MethodGet, "/v1/me/passkeys", nil, &keys)
	if len(keys) != 2 || keys[1].Name != "Laptop" {
		t.Fatalf("passkeys %+v", keys)
	}
	admin.ok(http.MethodDelete, "/v1/me/passkeys/"+keys[0].Id, nil, nil)
	if rec := admin.login(); rec.Code != http.StatusOK { // signs in with the newest (kept) key
		t.Fatalf("login with remaining key = %d", rec.Code)
	}

	// Two sessions; revoke the other one.
	phone := e.client()
	phone.auth = admin.auth
	if rec := phone.login(); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	var sessions []Session
	admin.ok(http.MethodGet, "/v1/me/sessions", nil, &sessions)
	var other Session
	current := 0
	for _, s := range sessions {
		if s.Current {
			current++
		} else {
			other = s
		}
	}
	if current != 1 || len(sessions) < 2 {
		t.Fatalf("sessions %+v", sessions)
	}
	// Someone else's session id is not found.
	stranger := e.client()
	stranger.fail(http.MethodDelete, "/v1/me/sessions/"+other.Id.String(), nil, http.StatusUnauthorized, "unauthenticated")
	admin.ok(http.MethodDelete, "/v1/me/sessions/"+other.Id.String(), nil, nil)
	if e.auditCount("auth.session.revoke") != 1 {
		t.Fatal("revoke not audited")
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()
	now := time.Now()
	e.svc.SetClock(func() time.Time { return now })

	// Sliding: use after two minutes pushes the expiry out and re-sends the cookie.
	now = now.Add(2 * time.Minute)
	rec := admin.do(http.MethodGet, "/v1/me", nil)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("slide: %d, cookies %v", rec.Code, rec.Result().Cookies())
	}
	// Past expiry (30 days without use): signed out and the cookie cleared.
	now = now.Add(31 * 24 * time.Hour)
	admin.fail(http.MethodGet, "/v1/me", nil, http.StatusUnauthorized, "unauthenticated")
	if admin.cookie != "" {
		t.Fatal("expired cookie not cleared")
	}
}

func TestLastAdminStays(t *testing.T) {
	e := newEnv(t)
	admin, me := e.setupAdmin()
	admin.fail(http.MethodPatch, "/v1/admin/users/"+me.User.Id.String(), map[string]any{"systemRole": "user"}, http.StatusConflict, "last_admin")
	admin.fail(http.MethodPatch, "/v1/admin/users/"+me.User.Id.String(), map[string]any{"status": "disabled"}, http.StatusConflict, "last_admin")
}

// TestAuthzMatrix checks x-roles for each kind of caller on representative
// operations.
func TestAuthzMatrix(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.setupAdmin()
	if err := settings.SetValue(e.t.Context(), e.store, identity.InviteRequiresApproval, false, nil); err != nil {
		t.Fatal(err)
	}
	member := e.client()
	member.enroll("/v1/invites/accept/begin", map[string]any{"code": e.invite(admin, "member").Code, "displayName": "M"}, "/v1/invites/accept/finish")
	owner := e.client()
	owner.enroll("/v1/invites/accept/begin", map[string]any{"code": e.invite(admin, "owner").Code, "displayName": "O"}, "/v1/invites/accept/finish")
	if err := settings.SetValue(e.t.Context(), e.store, identity.InviteRequiresApproval, true, nil); err != nil {
		t.Fatal(err)
	}
	pending := e.client()
	pending.enroll("/v1/invites/accept/begin", map[string]any{"code": e.invite(admin, "member").Code, "displayName": "P"}, "/v1/invites/accept/finish")
	anon := e.client()

	ops := []struct {
		method, path string
		roles        string // callers allowed, of a(dmin) o(wner) m(ember) p(ending) n(obody signed in)
	}{
		{http.MethodGet, "/v1/setup", "aompn"},
		{http.MethodGet, "/v1/me", "aomp"},
		{http.MethodGet, "/v1/me/sessions", "aomp"},
		{http.MethodGet, "/v1/me/passkeys", "aom"},
		{http.MethodGet, "/v1/invites", "ao"},
		{http.MethodGet, "/v1/admin/users", "a"},
		{http.MethodGet, "/v1/admin/households", "a"},
	}
	callers := map[byte]*client{'a': admin, 'o': owner, 'm': member, 'p': pending, 'n': anon}
	for _, op := range ops {
		for k, c := range callers {
			rec := c.do(op.method, op.path, nil)
			allowed := strings.IndexByte(op.roles, k) >= 0
			switch {
			case allowed && rec.Code != http.StatusOK:
				t.Errorf("%c %s %s = %d, want 200: %s", k, op.method, op.path, rec.Code, rec.Body)
			case !allowed && k == 'n' && rec.Code != http.StatusUnauthorized:
				t.Errorf("anonymous %s %s = %d, want 401", op.method, op.path, rec.Code)
			case !allowed && k != 'n' && rec.Code != http.StatusForbidden:
				t.Errorf("%c %s %s = %d, want 403", k, op.method, op.path, rec.Code)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
