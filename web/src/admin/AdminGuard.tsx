import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../api/problem'
import { Page } from '../app/Page'
import { useOverview } from './api'

/**
 * Admin-only gate. The server is the authority (every /api/v1/admin call is admin-only), so
 * the gate asks it: signed-out users are sent to sign-in by the API client's 401 handling,
 * signed-in non-admins see this page instead of a broken panel.
 */
export function AdminGuard({ children }: { children: ReactNode }) {
  const { error, isPending } = useOverview()
  if (error instanceof ApiError && (error.status === 403 || error.status === 401)) {
    return (
      <Page title="Admins only">
        <p className="t-body m-0 text-ink-muted">
          {error.status === 403
            ? 'This area is for system administrators. Ask an admin if you need access.'
            : 'Sign in to continue.'}
        </p>
        <Link to="/" className="font-semibold text-brand">
          Back to the app
        </Link>
      </Page>
    )
  }
  if (isPending) {
    return (
      <p role="status" className="m-0 px-4 py-6 text-ink-muted md:px-8">
        Checking access…
      </p>
    )
  }
  return children
}
