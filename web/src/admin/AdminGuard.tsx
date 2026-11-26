import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { Page } from '../app/Page'
import { useMe } from '../features/auth/session'

/**
 * Admin-only gate (UX only: the server enforces admin on every /api/v1/admin call).
 * Runs inside RequireSession, so a session exists; non-admins see this page instead of
 * a broken panel.
 */
export function AdminGuard({ children }: { children: ReactNode }) {
  const me = useMe().data
  if (me?.user.systemRole !== 'admin') {
    return (
      <Page title="Admins only">
        <p className="t-body m-0 text-ink-muted">
          This area is for system administrators. Ask an admin if you need access.
        </p>
        <Link to="/" className="font-semibold text-brand">
          Back to the app
        </Link>
      </Page>
    )
  }
  return children
}
