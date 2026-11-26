// Package passkey is the WebAuthn identity provider: discoverable (resident)
// credentials with user verification required, so a passkey alone signs in.
//
// A subject is a random 32-byte WebAuthn user handle (base64url), never the
// app user id, so credentials carry no account identifier.
package passkey

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/identity"
)

// ProviderID is this provider's identity_link.provider value.
const ProviderID = "passkey"

// ceremonyTTL bounds a ceremony; browsers time the prompt out sooner.
const ceremonyTTL = 5 * time.Minute

// Provider implements identity.Provider and identity.CredentialManager.
type Provider struct {
	wa   *webauthn.WebAuthn
	pool *pgxpool.Pool
}

var (
	_ identity.Provider          = (*Provider)(nil)
	_ identity.CredentialManager = (*Provider)(nil)
)

// New returns a provider for the app at origin (STOCKPOT_PUBLIC_URL). The
// relying party id is its host name, so passkeys are bound to that domain.
func New(pool *pgxpool.Pool, origin *url.URL) (*Provider, error) {
	sel := protocol.AuthenticatorSelection{
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		RequireResidentKey: protocol.ResidentKeyRequired(),
		UserVerification:   protocol.VerificationRequired,
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                   origin.Hostname(),
		RPDisplayName:          "Stockpot",
		RPOrigins:              []string{origin.Scheme + "://" + origin.Host},
		AttestationPreference:  protocol.PreferNoAttestation,
		AuthenticatorSelection: sel,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("passkey: %w", err)
	}
	return &Provider{wa: wa, pool: pool}, nil
}

// ID implements identity.Provider.
func (*Provider) ID() string { return ProviderID }

// Kind implements identity.Provider.
func (*Provider) Kind() identity.Kind { return identity.KindInteractive }

// BeginLogin starts a discoverable login: the authenticator offers whichever
// Stockpot passkeys it holds.
func (p *Provider) BeginLogin(context.Context, identity.LoginStart) (identity.Challenge, error) {
	assertion, session, err := p.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return identity.Challenge{}, fmt.Errorf("passkey: begin login: %w", err)
	}
	return challenge(assertion.Response, session)
}

// FinishLogin verifies an assertion and records the credential's use.
func (p *Provider) FinishLogin(ctx context.Context, in identity.LoginFinish) (identity.Identity, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(in.State, &session); err != nil {
		return identity.Identity{}, fmt.Errorf("passkey: state: %w", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Response)
	if err != nil {
		return identity.Identity{}, rejected(err)
	}
	q := dbq.New(p.pool)
	var owner *user
	_, cred, err := p.wa.ValidatePasskeyLogin(func(_, userHandle []byte) (webauthn.User, error) {
		u, err := p.loadUser(ctx, q, userHandle)
		if err != nil {
			return nil, err
		}
		if len(u.creds) == 0 {
			return nil, errors.New("unknown user handle")
		}
		owner = u
		return u, nil
	}, session, parsed)
	if err != nil {
		return identity.Identity{}, rejected(err)
	}
	if cred.Authenticator.CloneWarning {
		return identity.Identity{}, rejected(errors.New("signature counter went backwards (cloned authenticator?)"))
	}
	if err := q.UpdateWebauthnCredentialUse(ctx, dbq.UpdateWebauthnCredentialUseParams{
		ID:           cred.ID,
		SignCount:    int64(cred.Authenticator.SignCount),
		BackupState:  cred.Flags.BackupState,
		UserVerified: cred.Flags.UserVerified,
	}); err != nil {
		return identity.Identity{}, fmt.Errorf("passkey: record use: %w", err)
	}
	return identity.Identity{Provider: ProviderID, Subject: encode(owner.handle)}, nil
}

// BeginEnroll starts creating a passkey for in.Subject (or a new subject).
func (p *Provider) BeginEnroll(ctx context.Context, in identity.EnrollStart) (identity.Challenge, error) {
	u := &user{name: in.Name, displayName: in.DisplayName}
	if in.Subject == "" {
		u.handle = make([]byte, 32)
		_, _ = rand.Read(u.handle)
	} else {
		h, err := decode(in.Subject)
		if err != nil {
			return identity.Challenge{}, fmt.Errorf("passkey: subject: %w", err)
		}
		existing, err := p.loadUser(ctx, dbq.New(p.pool), h)
		if err != nil {
			return identity.Challenge{}, err
		}
		u.handle, u.creds = h, existing.creds
	}
	creation, session, err := p.wa.BeginRegistration(u,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
	)
	if err != nil {
		return identity.Challenge{}, fmt.Errorf("passkey: begin enrol: %w", err)
	}
	return challenge(creation.Response, session)
}

// FinishEnroll verifies the attestation and stores the new credential in
// in.Tx.
func (p *Provider) FinishEnroll(ctx context.Context, in identity.EnrollFinish) (identity.Identity, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(in.State, &session); err != nil {
		return identity.Identity{}, fmt.Errorf("passkey: state: %w", err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(in.Response)
	if err != nil {
		return identity.Identity{}, rejected(err)
	}
	cred, err := p.wa.CreateCredential(&user{handle: session.UserID}, session, parsed)
	if err != nil {
		return identity.Identity{}, rejected(err)
	}
	tx := in.Tx
	if tx == nil {
		tx = p.pool
	}
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	var aaguid *uuid.UUID
	if id, err := uuid.FromBytes(cred.Authenticator.AAGUID); err == nil && id != uuid.Nil {
		aaguid = &id
	}
	label := in.Label
	if label == "" {
		label = "Passkey"
	}
	if err := dbq.New(tx).InsertWebauthnCredential(ctx, dbq.InsertWebauthnCredentialParams{
		ID:              cred.ID,
		UserHandle:      session.UserID,
		Name:            truncate(label, 60),
		PublicKey:       cred.PublicKey,
		AttestationType: cred.AttestationType,
		Aaguid:          aaguid,
		SignCount:       int64(cred.Authenticator.SignCount),
		Transports:      transports,
		UserPresent:     cred.Flags.UserPresent,
		UserVerified:    cred.Flags.UserVerified,
		BackupEligible:  cred.Flags.BackupEligible,
		BackupState:     cred.Flags.BackupState,
	}); err != nil {
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "23505" /* unique_violation */ {
			return identity.Identity{}, rejected(errors.New("credential already registered"))
		}
		return identity.Identity{}, fmt.Errorf("passkey: store credential: %w", err)
	}
	return identity.Identity{Provider: ProviderID, Subject: encode(session.UserID)}, nil
}

// ListCredentials implements identity.CredentialManager.
func (p *Provider) ListCredentials(ctx context.Context, subject string) ([]identity.Credential, error) {
	h, err := decode(subject)
	if err != nil {
		return nil, err
	}
	rows, err := dbq.New(p.pool).ListWebauthnCredentials(ctx, h)
	if err != nil {
		return nil, err
	}
	out := make([]identity.Credential, len(rows))
	for i, r := range rows {
		out[i] = identity.Credential{
			ID:         encode(r.ID),
			Name:       r.Name,
			CreatedAt:  r.CreatedAt,
			LastUsedAt: r.LastUsedAt,
			Synced:     r.BackupState,
		}
	}
	return out, nil
}

// RemoveCredential implements identity.CredentialManager.
func (p *Provider) RemoveCredential(ctx context.Context, tx dbq.DBTX, subject, id string) error {
	h, err := decode(subject)
	if err != nil {
		return identity.ErrNotFound
	}
	credID, err := decode(id)
	if err != nil {
		return identity.ErrNotFound
	}
	n, err := dbq.New(tx).DeleteWebauthnCredential(ctx, dbq.DeleteWebauthnCredentialParams{ID: credID, UserHandle: h})
	if err != nil {
		return err
	}
	if n == 0 {
		return identity.ErrNotFound
	}
	return nil
}

func (p *Provider) loadUser(ctx context.Context, q *dbq.Queries, handle []byte) (*user, error) {
	rows, err := q.ListWebauthnCredentials(ctx, handle)
	if err != nil {
		return nil, err
	}
	u := &user{handle: handle}
	for _, r := range rows {
		ts := make([]protocol.AuthenticatorTransport, len(r.Transports))
		for i, t := range r.Transports {
			ts[i] = protocol.AuthenticatorTransport(t)
		}
		var aaguid []byte
		if r.Aaguid != nil {
			aaguid = r.Aaguid[:]
		}
		u.creds = append(u.creds, webauthn.Credential{
			ID:              r.ID,
			PublicKey:       r.PublicKey,
			AttestationType: r.AttestationType,
			Transport:       ts,
			Flags: webauthn.CredentialFlags{
				UserPresent:    r.UserPresent,
				UserVerified:   r.UserVerified,
				BackupEligible: r.BackupEligible,
				BackupState:    r.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				AAGUID:    aaguid,
				SignCount: uint32(r.SignCount), //nolint:gosec // stored from a uint32
			},
		})
	}
	return u, nil
}

// user adapts a subject to webauthn.User.
type user struct {
	handle      []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u *user) WebAuthnID() []byte                         { return u.handle }
func (u *user) WebAuthnName() string                       { return u.name }
func (u *user) WebAuthnDisplayName() string                { return u.displayName }
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func challenge(options any, session *webauthn.SessionData) (identity.Challenge, error) {
	opts, err := json.Marshal(options)
	if err != nil {
		return identity.Challenge{}, err
	}
	state, err := json.Marshal(session)
	if err != nil {
		return identity.Challenge{}, err
	}
	return identity.Challenge{Options: opts, State: state, TTL: ceremonyTTL}, nil
}

// rejected wraps a verification failure: the client sees only that the
// passkey wasn't accepted; the cause is kept for the log.
func rejected(cause error) error {
	return fmt.Errorf("%w: %w", identity.ErrCredentialInvalid, detail(cause))
}

// detail includes go-webauthn's DevInfo, which says what actually failed.
func detail(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return fmt.Errorf("%w (%s)", err, pe.DevInfo)
	}
	return err
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("not base64url")
	}
	return b, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
