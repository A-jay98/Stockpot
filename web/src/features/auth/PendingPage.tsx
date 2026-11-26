import { useQuery } from '@tanstack/react-query'
import { Clock } from 'lucide-react'
import { Navigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { Button } from '../../ui'
import { AuthLayout } from './AuthLayout'
import { meKey, useSignOut } from './session'

/** Waiting room for users an admin hasn't approved yet; checks back periodically. */
export function PendingPage() {
  const me = useQuery({
    queryKey: meKey,
    queryFn: () => unwrap(api.GET('/v1/me')),
    refetchInterval: 15_000,
    retry: false,
  })
  const signOut = useSignOut()

  if (me.isError) return <Navigate to="/login" replace />
  if (me.data && me.data.user.status !== 'pending') return <Navigate to="/" replace />

  return (
    <AuthLayout title="Waiting for approval">
      <div className="flex items-start gap-3 rounded-lg bg-surface p-4 shadow-card">
        <Clock className="mt-0.5 size-5 shrink-0 text-soon" aria-hidden />
        <p className="t-body m-0">
          {me.data ? `Thanks, ${me.data.user.displayName}. ` : ''}An admin needs to approve your
          account. This page updates by itself once they do.
        </p>
      </div>
      <Button variant="secondary" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
        Sign out
      </Button>
    </AuthLayout>
  )
}
