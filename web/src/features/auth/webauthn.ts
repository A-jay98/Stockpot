/**
 * WebAuthn glue: turns the server's JSON options (binary fields base64url)
 * into what navigator.credentials expects, and the resulting credential back
 * into JSON. Done by hand rather than with PublicKeyCredential.parse*FromJSON
 * / toJSON, which older Safari and Firefox lack.
 */

type Json = Record<string, unknown>

export function base64urlToBuffer(s: string): ArrayBuffer {
  const b64 = s
    .replace(/-/g, '+')
    .replace(/_/g, '/')
    .padEnd(Math.ceil(s.length / 4) * 4, '=')
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

export function bufferToBase64url(buf: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    buf instanceof ArrayBuffer
      ? new Uint8Array(buf)
      : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

type Descriptor = { id: string; type: string; transports?: string[] }

const descriptors = (list: unknown) =>
  ((list as Descriptor[] | undefined) ?? []).map((d) => ({
    ...d,
    id: base64urlToBuffer(d.id),
  })) as PublicKeyCredentialDescriptor[]

export function creationOptions(json: Json): PublicKeyCredentialCreationOptions {
  const user = json.user as Json
  return {
    ...(json as unknown as PublicKeyCredentialCreationOptions),
    challenge: base64urlToBuffer(json.challenge as string),
    user: {
      ...(user as unknown as PublicKeyCredentialUserEntity),
      id: base64urlToBuffer(user.id as string),
    },
    excludeCredentials: descriptors(json.excludeCredentials),
  }
}

export function requestOptions(json: Json): PublicKeyCredentialRequestOptions {
  return {
    ...(json as unknown as PublicKeyCredentialRequestOptions),
    challenge: base64urlToBuffer(json.challenge as string),
    allowCredentials: descriptors(json.allowCredentials),
  }
}

function credentialJSON(cred: PublicKeyCredential): Json {
  const r = cred.response
  const response: Json = { clientDataJSON: bufferToBase64url(r.clientDataJSON) }
  if ('attestationObject' in r) {
    const att = r as AuthenticatorAttestationResponse
    response.attestationObject = bufferToBase64url(att.attestationObject)
    response.transports = att.getTransports?.() ?? []
  } else {
    const a = r as AuthenticatorAssertionResponse
    response.authenticatorData = bufferToBase64url(a.authenticatorData)
    response.signature = bufferToBase64url(a.signature)
    if (a.userHandle) response.userHandle = bufferToBase64url(a.userHandle)
  }
  return {
    id: cred.id,
    rawId: bufferToBase64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response,
  }
}

export const passkeysSupported = () =>
  typeof window !== 'undefined' && typeof window.PublicKeyCredential === 'function'

/** Runs navigator.credentials.create with server options; returns the credential JSON. */
export async function createPasskey(options: Json): Promise<Json> {
  const cred = await navigator.credentials.create({ publicKey: creationOptions(options) })
  if (!cred) throw new Error('No passkey was created.')
  return credentialJSON(cred as PublicKeyCredential)
}

/** Runs navigator.credentials.get with server options; returns the credential JSON. */
export async function getPasskey(options: Json): Promise<Json> {
  const cred = await navigator.credentials.get({ publicKey: requestOptions(options) })
  if (!cred) throw new Error('No passkey was chosen.')
  return credentialJSON(cred as PublicKeyCredential)
}

/** A user cancelling the browser prompt is not an error worth shouting about. */
export const isCancelled = (e: unknown) =>
  e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')
