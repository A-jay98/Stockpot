import { useQueryClient } from '@tanstack/react-query'
import { KeyRound } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Button } from '../../ui'
import { AuthLayout, FormError } from './AuthLayout'
import { authErrorMessage } from './errors'
import { homeFor, meKey, signInWithPasskey, useSetupStatus } from './session'
import { passkeysSupported } from './webauthn'

/** Passkey sign-in; also the target of the API client's 401 redirect. */
export function LoginPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const setup = useSetupStatus()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  async function signIn() {
    setBusy(true)
    setError(undefined)
    try {
      const me = await signInWithPasskey()
      qc.setQueryData(meKey, me)
      await navigate(homeFor(me, params.get('next')), { replace: true })
    } catch (e) {
      setError(authErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthLayout title="Sign in">
      {setup.data?.needed ? (
        <p className="t-body m-0 text-ink-muted">
          Stockpot isn't set up yet. <Link to="/setup">Set it up</Link> to create the admin account.
        </p>
      ) : (
        <p className="t-body m-0 text-ink-muted">
          Use the passkey on this device, or scan with your phone.
        </p>
      )}
      {!passkeysSupported() && <FormError message="This browser doesn't support passkeys." />}
      <Button size="lg" block onClick={signIn} disabled={busy || !passkeysSupported()}>
        <KeyRound aria-hidden />
        {busy ? 'Waiting for passkey…' : 'Sign in with a passkey'}
      </Button>
      <FormError message={error} />
      <p className="t-body-sm m-0 text-ink-muted">
        New here? Ask someone in your household for an invite link.
      </p>
    </AuthLayout>
  )
}
