// Package passkeytest is a software WebAuthn authenticator for tests: it
// answers the server's creation and request options the way a platform
// passkey does (ES256, "none" attestation, discoverable, user verified).
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

// Authenticator holds passkeys for one origin.
type Authenticator struct {
	Origin string
	// SkipUV leaves the user-verified flag unset (the server must refuse).
	SkipUV bool
	creds  []*Credential
}

// Credential is a stored passkey.
type Credential struct {
	ID         []byte
	UserHandle []byte
	key        *ecdsa.PrivateKey
	signCount  uint32
}

// New returns an empty authenticator for origin (e.g. https://stockpot.test).
func New(origin string) *Authenticator { return &Authenticator{Origin: origin} }

// Credentials lists the stored passkeys, oldest first.
func (a *Authenticator) Credentials() []*Credential { return a.creds }

// Forget drops every passkey (a different device).
func (a *Authenticator) Forget() { a.creds = nil }

type creationOptions struct {
	Challenge string `json:"challenge"`
	RP        struct {
		ID string `json:"id"`
	} `json:"rp"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}

type requestOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
}

var b64 = base64.RawURLEncoding

// Create answers PublicKeyCredentialCreationOptions (JSON) with a new
// passkey, as navigator.credentials.create().toJSON() would.
func (a *Authenticator) Create(options []byte) (json.RawMessage, error) {
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	handle, err := b64.DecodeString(o.User.ID)
	if err != nil {
		return nil, fmt.Errorf("user.id: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	c := &Credential{ID: make([]byte, 16), UserHandle: handle, key: key}
	_, _ = rand.Read(c.ID)

	ecdh, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	raw := ecdh.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:65]})
	if err != nil {
		return nil, err
	}
	authData := a.authData(o.RP.ID, 0x40, 0)
	authData = append(authData, make([]byte, 16)...)                      // AAGUID: none
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(c.ID))) //nolint:gosec // 16 bytes
	authData = append(authData, c.ID...)
	authData = append(authData, cose...)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		return nil, err
	}
	cdj, err := a.clientData("webauthn.create", o.Challenge)
	if err != nil {
		return nil, err
	}
	a.creds = append(a.creds, c)
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.ID), "rawId": b64.EncodeToString(c.ID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cdj),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal", "hybrid"},
		},
	})
}

// Get answers PublicKeyCredentialRequestOptions (JSON) with the newest
// passkey, as a discoverable navigator.credentials.get() would.
func (a *Authenticator) Get(options []byte) (json.RawMessage, error) {
	if len(a.creds) == 0 {
		return nil, errors.New("no passkeys")
	}
	return a.GetWith(a.creds[len(a.creds)-1], options)
}

// GetWith signs the request with c.
func (a *Authenticator) GetWith(c *Credential, options []byte) (json.RawMessage, error) {
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	c.signCount++
	authData := a.authData(o.RPID, 0, c.signCount)
	cdj, err := a.clientData("webauthn.get", o.Challenge)
	if err != nil {
		return nil, err
	}
	cdh := sha256.Sum256(cdj)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.ID), "rawId": b64.EncodeToString(c.ID), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cdj),
			"authenticatorData": b64.EncodeToString(authData),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(c.UserHandle),
		},
	})
}

// authData builds rpIdHash | flags | signCount. Flags: user present, user
// verified (unless SkipUV), backup eligible and backed up (a synced
// passkey), plus extra (attested credential data).
func (a *Authenticator) authData(rpID string, extra byte, count uint32) []byte {
	h := sha256.Sum256([]byte(rpID))
	flags := byte(0x01|0x08|0x10) | extra
	if !a.SkipUV {
		flags |= 0x04
	}
	out := append(h[:], flags)
	return binary.BigEndian.AppendUint32(out, count)
}

func (a *Authenticator) clientData(typ, challenge string) ([]byte, error) {
	return json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
}
