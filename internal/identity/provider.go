// Package identity owns users, households, memberships, invites, sessions
// and the authorization policy. Credentials are verified by pluggable
// Providers (passkeys today; OIDC or LDAP later), each turning a ceremony
// into a verified (provider, subject) that identity_link maps to a user.
package identity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Kind is how a provider talks to the client.
type Kind string

// Provider kinds.
const (
	// KindInteractive runs a challenge/response in the client (WebAuthn).
	KindInteractive Kind = "interactive"
	// KindRedirect sends the browser to an external IdP (OIDC).
	KindRedirect Kind = "redirect"
	// KindPassword checks a username and password (LDAP).
	KindPassword Kind = "password"
)

// Provider verifies credentials. Providers are stateless between Begin and
// Finish: the core keeps Challenge.State server-side (auth_ceremony) and
// hands it back exactly once.
type Provider interface {
	ID() string // "passkey"; later "oidc:<name>", "ldap"
	Kind() Kind
	BeginLogin(ctx context.Context, in LoginStart) (Challenge, error)
	FinishLogin(ctx context.Context, in LoginFinish) (Identity, error)
	// BeginEnroll starts adding a credential to an existing subject, or to a
	// new one when EnrollStart.Subject is empty.
	BeginEnroll(ctx context.Context, in EnrollStart) (Challenge, error)
	FinishEnroll(ctx context.Context, in EnrollFinish) (Identity, error)
}

// LoginStart parameterises a login. Passkey login is discoverable, so it
// needs nothing; password and redirect providers will add fields.
type LoginStart struct{}

// EnrollStart parameterises an enrolment.
type EnrollStart struct {
	// Subject to add a credential to; empty mints a new subject.
	Subject string
	// Name and DisplayName label the credential on the authenticator.
	Name        string
	DisplayName string
}

// Challenge is a started ceremony.
type Challenge struct {
	// Options go to the client (for passkeys, PublicKeyCredential*Options).
	Options json.RawMessage
	// RedirectURL is where a KindRedirect provider sends the browser.
	RedirectURL string
	// State stays on the server until Finish.
	State []byte
	// TTL bounds how long the ceremony may take.
	TTL time.Duration
}

// LoginFinish carries the client's answer to a login challenge.
type LoginFinish struct {
	State    []byte
	Response json.RawMessage
}

// EnrollFinish carries the client's answer to an enrolment challenge.
type EnrollFinish struct {
	State    []byte
	Response json.RawMessage
	// Label names the new credential for the user ("iPhone").
	Label string
	// Tx is the transaction the core enrols in, so a provider that stores
	// credentials commits them together with the user and link.
	Tx dbq.DBTX
}

// Identity is a verified (provider, subject).
type Identity struct {
	Provider string
	Subject  string
}

// Credential is one stored credential, as the user's security page lists it.
type Credential struct {
	ID         string // provider-scoped, URL-safe
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// Synced reports a credential backed up to a cloud keychain.
	Synced bool
}

// CredentialManager is implemented by providers that store credentials the
// user can list and remove (passkeys; not OIDC).
type CredentialManager interface {
	ListCredentials(ctx context.Context, subject string) ([]Credential, error)
	// RemoveCredential deletes one credential of subject; ErrNotFound if it
	// isn't theirs.
	RemoveCredential(ctx context.Context, tx dbq.DBTX, subject, id string) error
}
