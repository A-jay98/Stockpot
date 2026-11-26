import type { RouteObject } from 'react-router'
import { AdminGuard } from './AdminGuard'
import { AdminLayout } from './AdminLayout'
import { AdminSectionPage } from './AdminSectionPage'
import { JobsPage } from './JobsPage'
import { LLMPage } from './LLMPage'
import { LogsPage } from './LogsPage'
import { OverviewPage } from './OverviewPage'
import { SettingsPage } from './SettingsPage'

const placeholders: Record<string, [string, string]> = {
  users: ['Users', 'Pending approvals, invites, users, roles and sessions.'],
  audit: ['Audit log', 'Every security event and change, filterable by user, action and time.'],
}

/** `/admin` route group, admin-only (AdminGuard). */
export const adminRoutes: RouteObject = {
  path: 'admin',
  element: (
    <AdminGuard>
      <AdminLayout />
    </AdminGuard>
  ),
  children: [
    { index: true, Component: OverviewPage },
    { path: 'logs', Component: LogsPage },
    { path: 'jobs', Component: JobsPage },
    { path: 'settings', Component: SettingsPage },
    { path: 'llm', Component: LLMPage },
    ...Object.entries(placeholders).map(([path, [title, text]]) => ({
      path,
      element: <AdminSectionPage title={title} about={text} />,
    })),
  ],
}
