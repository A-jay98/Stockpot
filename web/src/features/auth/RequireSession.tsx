import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router'
import { useMe, useSetupStatus } from './session'

/**
 * Gate for signed-in screens: no session → sign-in (or first-run setup),
 * pending approval → waiting room. Admin-only screens add their own guard.
 */
export function RequireSession({ children }: { children: ReactNode }) {
  const me = useMe()
  const setup = useSetupStatus()
  const { pathname, search } = useLocation()

  if (me.isPending) return <div aria-busy="true" className="min-h-dvh" />
  if (me.isError) throw me.error
  if (!me.data) {
    if (setup.data?.needed) return <Navigate to="/setup" replace />
    if (setup.isPending) return <div aria-busy="true" className="min-h-dvh" />
    return <Navigate to={`/login?next=${encodeURIComponent(pathname + search)}`} replace />
  }
  if (me.data.user.status === 'pending') return <Navigate to="/pending" replace />
  return children
}
