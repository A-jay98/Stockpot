import { useQueryClient } from '@tanstack/react-query'
import { type FormEvent, useState } from 'react'
import { Navigate, useNavigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Button, Field, Input } from '../../ui'
import { AuthLayout, FormError } from './AuthLayout'
import { authErrorMessage } from './errors'
import { enroll, meKey, useSetupStatus } from './session'

/** First run: claim the admin account with the server's setup token. */
export function SetupPage() {
  const status = useSetupStatus()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [token, setToken] = useState('')
  const [name, setName] = useState('')
  const [household, setHousehold] = useState('Home')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  if (status.data && !status.data.needed) return <Navigate to="/login" replace />

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const me = await enroll(
        () =>
          unwrap(
            api.POST('/v1/setup/begin', {
              body: { token, displayName: name, householdName: household },
            }),
          ),
        (body) => unwrap(api.POST('/v1/setup/finish', { body })),
      )
      qc.setQueryData(meKey, me)
      // Leave before the setup status refetches, or this page's own
      // "already set up" redirect would win.
      await navigate('/', { replace: true })
      void qc.invalidateQueries({ queryKey: ['setup'] })
    } catch (err) {
      setError(authErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthLayout title="Set up Stockpot">
      <p className="t-body m-0 text-ink-muted">
        Create the admin account and your household. You'll sign in with a passkey, no password.
      </p>
      {status.data && !status.data.tokenConfigured && (
        <FormError message="The server has no admin setup token configured." />
      )}
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label="Setup token" hint="From the admin-setup-token credential on the server.">
          {(p) => (
            <Input
              {...p}
              type="password"
              autoComplete="off"
              required
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
          )}
        </Field>
        <Field label="Your name">
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
        <Field label="Household name">
          {(p) => (
            <Input
              {...p}
              required
              maxLength={80}
              value={household}
              onChange={(e) => setHousehold(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" size="lg" block disabled={busy}>
          {busy ? 'Waiting for passkey…' : 'Create passkey and finish'}
        </Button>
        <FormError message={error} />
      </form>
    </AuthLayout>
  )
}
