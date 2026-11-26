package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Service is the identity core: it runs ceremonies through providers, maps
// identities to users, and owns users, households, invites and sessions.
type Service struct {
	pool      *pgxpool.Pool
	q         *dbq.Queries
	settings  *settings.Store
	providers map[string]Provider
	// enroller creates credentials for new users (setup, invites).
	enroller       Provider
	setupTokenHash []byte // nil: no setup token configured
	publicURL      *url.URL
	now            func() time.Time
}

// Deps configure a Service.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	// Enroller is the provider new users enrol with (passkeys). Further
	// providers (OIDC, LDAP) go in Providers.
	Enroller  Provider
	Providers []Provider
	// SetupToken claims the first admin account; empty disables /setup.
	SetupToken string
	// PublicURL builds invite links.
	PublicURL *url.URL
}

// NewService returns the identity core.
func NewService(d Deps) (*Service, error) {
	if d.Pool == nil || d.Settings == nil || d.Enroller == nil || d.PublicURL == nil {
		return nil, errors.New("identity: Pool, Settings, Enroller and PublicURL are required")
	}
	s := &Service{
		pool:      d.Pool,
		q:         dbq.New(d.Pool),
		settings:  d.Settings,
		providers: map[string]Provider{},
		enroller:  d.Enroller,
		publicURL: d.PublicURL,
		now:       time.Now,
	}
	for _, p := range append([]Provider{d.Enroller}, d.Providers...) {
		if _, dup := s.providers[p.ID()]; dup {
			return nil, fmt.Errorf("identity: provider %s registered twice", p.ID())
		}
		s.providers[p.ID()] = p
	}
	if d.SetupToken != "" {
		h := sha256.Sum256([]byte(d.SetupToken))
		s.setupTokenHash = h[:]
	}
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// --- ceremonies -------------------------------------------------------------

// Ceremony purposes.
const (
	purposeSetup      = "setup"
	purposeLogin      = "login"
	purposeInvite     = "invite"
	purposeAddPasskey = "add_passkey"
)

// CeremonyStart is a started ceremony as the client sees it.
type CeremonyStart struct {
	ID        uuid.UUID
	Options   json.RawMessage
	ExpiresAt time.Time
}

func (s *Service) startCeremony(ctx context.Context, purpose string, p Provider, ch Challenge, data any) (CeremonyStart, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return CeremonyStart{}, err
	}
	if _, err := s.q.PruneExpiredCeremonies(ctx); err != nil {
		return CeremonyStart{}, fmt.Errorf("prune ceremonies: %w", err)
	}
	id := uuid.Must(uuid.NewV7())
	ttl := ch.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	expires := s.now().Add(ttl)
	if err := s.q.InsertCeremony(ctx, dbq.InsertCeremonyParams{
		ID: id, Purpose: purpose, Provider: p.ID(), State: ch.State, Context: raw, ExpiresAt: expires,
	}); err != nil {
		return CeremonyStart{}, fmt.Errorf("store ceremony: %w", err)
	}
	return CeremonyStart{ID: id, Options: ch.Options, ExpiresAt: expires}, nil
}

// takeCeremony consumes a ceremony: whatever happens next, it can't be
// finished twice.
func (s *Service) takeCeremony(ctx context.Context, id uuid.UUID, purpose string, data any) (dbq.AuthCeremony, Provider, error) {
	row, err := s.q.TakeCeremony(ctx, dbq.TakeCeremonyParams{ID: id, Purpose: purpose})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, nil, ErrCeremonyInvalid
	}
	if err != nil {
		return row, nil, err
	}
	if !row.ExpiresAt.After(s.now()) {
		return row, nil, ErrCeremonyInvalid
	}
	p, ok := s.providers[row.Provider]
	if !ok {
		return row, nil, ErrCeremonyInvalid
	}
	if data != nil {
		if err := json.Unmarshal(row.Context, data); err != nil {
			return row, nil, fmt.Errorf("ceremony context: %w", err)
		}
	}
	return row, p, nil
}

// --- setup --------------------------------------------------------------------

// SetupStatus reports whether the first admin still has to be claimed, and
// whether a setup token is configured to claim it with.
func (s *Service) SetupStatus(ctx context.Context) (needed, configured bool, err error) {
	exists, err := s.q.AdminExists(ctx)
	if err != nil {
		return false, false, err
	}
	return !exists, s.setupTokenHash != nil, nil
}

type setupContext struct {
	DisplayName   string `json:"displayName"`
	HouseholdName string `json:"householdName"`
}

// BeginSetup checks the admin setup token and starts enrolling the first
// admin's passkey.
func (s *Service) BeginSetup(ctx context.Context, token, displayName, householdName string) (CeremonyStart, error) {
	if s.setupTokenHash == nil {
		return CeremonyStart{}, ErrSetupNotEnabled
	}
	exists, err := s.q.AdminExists(ctx)
	if err != nil {
		return CeremonyStart{}, err
	}
	if exists {
		return CeremonyStart{}, ErrSetupUnavailable
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(token)))
	if subtle.ConstantTimeCompare(got[:], s.setupTokenHash) != 1 {
		if err := audit.Record(ctx, s.pool, "auth.setup.token_rejected", audit.Target{}, nil, nil); err != nil {
			return CeremonyStart{}, err
		}
		return CeremonyStart{}, ErrSetupToken
	}
	displayName, err = cleanName("displayName", displayName)
	if err != nil {
		return CeremonyStart{}, err
	}
	householdName, err = cleanName("householdName", householdName)
	if err != nil {
		return CeremonyStart{}, err
	}
	ch, err := s.enroller.BeginEnroll(ctx, EnrollStart{Name: displayName, DisplayName: displayName})
	if err != nil {
		return CeremonyStart{}, err
	}
	return s.startCeremony(ctx, purposeSetup, s.enroller, ch, setupContext{DisplayName: displayName, HouseholdName: householdName})
}

// setupLockID serializes setup so two racing ceremonies can't both create
// an admin.
const setupLockID int64 = 0x73746f636b736574 // "stockset"

// FinishSetup enrols the passkey and creates the first admin, their
// household and a session.
func (s *Service) FinishSetup(ctx context.Context, ceremony uuid.UUID, response json.RawMessage, label string) (SessionToken, error) {
	var sc setupContext
	row, p, err := s.takeCeremony(ctx, ceremony, purposeSetup, &sc)
	if err != nil {
		return SessionToken{}, err
	}
	var tok SessionToken
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", setupLockID); err != nil {
			return err
		}
		q := s.q.WithTx(tx)
		exists, err := q.AdminExists(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrSetupUnavailable
		}
		ident, err := p.FinishEnroll(ctx, EnrollFinish{State: row.State, Response: response, Label: label, Tx: tx})
		if err != nil {
			return err
		}
		now := s.now()
		user, err := q.CreateUser(ctx, dbq.CreateUserParams{
			ID: uuid.Must(uuid.NewV7()), DisplayName: sc.DisplayName,
			Status: StatusActive, SystemRole: SystemAdmin, ApprovedAt: &now,
		})
		if err != nil {
			return err
		}
		if err := s.link(ctx, q, user.ID, ident); err != nil {
			return err
		}
		hh, err := q.CreateHousehold(ctx, dbq.CreateHouseholdParams{ID: uuid.Must(uuid.NewV7()), Name: sc.HouseholdName, CreatedBy: &user.ID})
		if err != nil {
			return err
		}
		if err := q.AddMembership(ctx, dbq.AddMembershipParams{UserID: user.ID, HouseholdID: hh.ID, Role: HouseholdOwner}); err != nil {
			return err
		}
		actx := audit.WithActor(ctx, user.ID, hh.ID)
		if err := audit.Record(actx, tx, "auth.setup.complete", audit.UserTarget(user.ID), nil,
			map[string]any{"user": userAudit(user), "household": map[string]any{"id": hh.ID, "name": hh.Name}}); err != nil {
			return err
		}
		if err := audit.Record(actx, tx, "auth.passkey.add", audit.UserTarget(user.ID), nil, map[string]any{"provider": ident.Provider}); err != nil {
			return err
		}
		tok, err = s.createSession(actx, q, user, hh.ID, ident.Provider)
		return err
	})
	return tok, err
}

// --- login ---------------------------------------------------------------------

// BeginLogin starts a passkey sign-in.
func (s *Service) BeginLogin(ctx context.Context) (CeremonyStart, error) {
	ch, err := s.enroller.BeginLogin(ctx, LoginStart{})
	if err != nil {
		return CeremonyStart{}, err
	}
	return s.startCeremony(ctx, purposeLogin, s.enroller, ch, struct{}{})
}

// FinishLogin verifies the passkey and opens a session. Pending users get a
// session too, so they can watch for approval; the policy limits what it
// can do.
func (s *Service) FinishLogin(ctx context.Context, ceremony uuid.UUID, response json.RawMessage) (SessionToken, error) {
	row, p, err := s.takeCeremony(ctx, ceremony, purposeLogin, nil)
	if err != nil {
		return SessionToken{}, err
	}
	ident, err := p.FinishLogin(ctx, LoginFinish{State: row.State, Response: response})
	if err != nil {
		if errors.Is(err, ErrCredentialInvalid) {
			if aerr := audit.Record(ctx, s.pool, "auth.login.failed", audit.Target{},
				nil, map[string]any{"provider": p.ID(), "reason": err.Error()}); aerr != nil {
				return SessionToken{}, aerr
			}
		}
		return SessionToken{}, err
	}
	link, err := s.q.GetIdentityLink(ctx, dbq.GetIdentityLinkParams{Provider: ident.Provider, Subject: ident.Subject})
	if errors.Is(err, pgx.ErrNoRows) {
		// A credential whose account is gone (or never finished enrolling).
		if aerr := audit.Record(ctx, s.pool, "auth.login.failed", audit.Target{},
			nil, map[string]any{"provider": p.ID(), "reason": "no account for this passkey"}); aerr != nil {
			return SessionToken{}, aerr
		}
		return SessionToken{}, ErrCredentialInvalid
	}
	if err != nil {
		return SessionToken{}, err
	}
	user, err := s.q.GetUser(ctx, link.UserID)
	if err != nil {
		return SessionToken{}, err
	}
	actx := audit.WithActor(ctx, user.ID, uuid.Nil)
	if user.Status == StatusDisabled {
		if err := audit.Record(actx, s.pool, "auth.login.denied", audit.UserTarget(user.ID), nil, map[string]any{"reason": "disabled"}); err != nil {
			return SessionToken{}, err
		}
		return SessionToken{}, ErrAccountDisabled
	}
	var tok SessionToken
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		hh, _, err := s.defaultHousehold(ctx, q, user.ID)
		if err != nil {
			return err
		}
		if err := q.TouchUserLogin(ctx, user.ID); err != nil {
			return err
		}
		if err := q.TouchIdentityLink(ctx, link.ID); err != nil {
			return err
		}
		actx := audit.WithActor(ctx, user.ID, hh)
		if err := audit.Record(actx, tx, "auth.login", audit.UserTarget(user.ID), nil, map[string]any{"provider": ident.Provider}); err != nil {
			return err
		}
		tok, err = s.createSession(actx, q, user, hh, ident.Provider)
		return err
	})
	return tok, err
}

// --- helpers --------------------------------------------------------------------

func (s *Service) link(ctx context.Context, q *dbq.Queries, user uuid.UUID, ident Identity) error {
	return q.CreateIdentityLink(ctx, dbq.CreateIdentityLinkParams{
		ID: uuid.Must(uuid.NewV7()), UserID: user, Provider: ident.Provider, Subject: ident.Subject, Data: []byte("{}"),
	})
}

// defaultHousehold is the household a new session starts in: the user's
// first membership.
func (s *Service) defaultHousehold(ctx context.Context, q *dbq.Queries, user uuid.UUID) (uuid.UUID, string, error) {
	ms, err := q.ListMembershipsForUser(ctx, user)
	if err != nil || len(ms) == 0 {
		return uuid.Nil, "", err
	}
	return ms[0].HouseholdID, ms[0].Role, nil
}

func cleanName(field, s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", &ValidationError{Field: field, Msg: "is required"}
	case n > 80:
		return "", &ValidationError{Field: field, Msg: "must be at most 80 characters"}
	}
	return s, nil
}

// userAudit is the part of a user an audit diff shows.
func userAudit(u dbq.AppUser) map[string]any {
	return map[string]any{"id": u.ID, "displayName": u.DisplayName, "status": u.Status, "systemRole": u.SystemRole}
}

// clientIP is the request's address for session rows.
func clientIP(ctx context.Context) *netip.Addr {
	info := reqctx.From(ctx)
	if info == nil || info.ClientIP == "" {
		return nil
	}
	ip, err := netip.ParseAddr(info.ClientIP)
	if err != nil {
		return nil
	}
	return &ip
}

func randomToken(n int) (string, []byte) {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	return tok, h[:]
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}
