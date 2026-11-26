import { useQuery, useQueryClient } from '@tanstack/react-query'
import { type FormEvent, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Button, Field, Input } from '../../ui'
import { AuthLayout, FormError } from './AuthLayout'
import { authErrorMessage } from './errors'
import { enroll, homeFor, type Me, meKey, useMe } from './session'

function decodeCode(raw: string) {
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw // malformed escapes: let the server call it invalid
  }
}

/** /invite#<code>: create an account (or join, if signed in) from an invite link. */
export function InvitePage() {
  const { hash, search } = useLocation()
  // The code travels in the fragment so it never reaches a server log.
  const code = decodeCode(hash.replace(/^#/, '')) || new URLSearchParams(search).get('code') || ''
  const me = useMe()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const info = useQuery({
    queryKey: ['invite', code],
    queryFn: () => unwrap(api.POST('/v1/invites/inspect', { body: { code } })),
    enabled: code !== '',
    retry: false,
  })
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  async function run(action: () => Promise<Me>) {
    setBusy(true)
    setError(undefined)
    try {
      const account = await action()
      // A new account or a newly active household: nothing cached still applies.
      qc.clear()
      qc.setQueryData(meKey, account)
      await navigate(homeFor(account), { replace: true })
    } catch (e) {
      setError(authErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const accept = (e: FormEvent) => {
    e.preventDefault()
    void run(() =>
      enroll(
        () => unwrap(api.POST('/v1/invites/accept/begin', { body: { code, displayName: name } })),
        (body) => unwrap(api.POST('/v1/invites/accept/finish', { body })),
      ),
    )
  }
  const join = () => void run(() => unwrap(api.POST('/v1/invites/join', { body: { code } })))

  if (!code || info.isError) {
    return (
      <AuthLayout title="Invite">
        <FormError
          message={
            info.error ? authErrorMessage(info.error) : 'This link is missing its invite code.'
          }
        />
        <Link to="/login">Go to sign in</Link>
      </AuthLayout>
    )
  }
  if (!info.data) return <AuthLayout title="Invite">{null}</AuthLayout>

  const roleText = info.data.role === 'owner' ? 'as an owner' : 'as a member'
  if (me.data?.user.status === 'pending') {
    return (
      <AuthLayout title={`Join ${info.data.householdName}`}>
        <p className="t-body m-0 text-ink-muted">
          Your account is still waiting for approval. Open this link again once an admin approves
          it.
        </p>
        <Link to="/pending">Back</Link>
      </AuthLayout>
    )
  }
  const signedIn = me.data?.user.status === 'active'
  return (
    <AuthLayout title={`Join ${info.data.householdName}`}>
      <p className="t-body m-0 text-ink-muted">
        You've been invited to {info.data.householdName} {roleText}.
      </p>
      {signedIn ? (
        <>
          <Button size="lg" block onClick={join} disabled={busy}>
            Join as {me.data?.user.displayName}
          </Button>
          <FormError message={error} />
        </>
      ) : (
        <form onSubmit={accept} className="flex flex-col gap-4">
          <Field label="Your name" hint="How others in the household will see you.">
            {(p) => (
              <Input
                {...p}
                autoComplete="name"
                required
                maxLength={80}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <Button type="submit" size="lg" block disabled={busy}>
            {busy ? 'Waiting for passkey…' : 'Create passkey and join'}
          </Button>
          <FormError message={error} />
          <p className="t-body-sm m-0 text-ink-muted">
            Already have an account? <Link to="/login">Sign in</Link>, then open this link again.
          </p>
        </form>
      )}
    </AuthLayout>
  )
}
