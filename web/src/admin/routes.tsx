import type { RouteObject } from 'react-router'
import { AccessPage } from './AccessPage'
import { AdminGuard } from './AdminGuard'
import { AdminLayout } from './AdminLayout'
import { AdminSectionPage } from './AdminSectionPage'
import { AuditPage } from './AuditPage'
import { JobsPage } from './JobsPage'
import { LogsPage } from './LogsPage'
import { OverviewPage } from './OverviewPage'
import { SettingsPage } from './SettingsPage'
import { UsersPage } from './UsersPage'

const placeholders: Record<string, [string, string]> = {
  llm: ['LLM', 'Providers, keys, models, daily caps and usage.'],
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
    { path: 'users', Component: UsersPage },
    { path: 'audit', Component: AuditPage },
    { path: 'access', Component: AccessPage },
    { path: 'logs', Component: LogsPage },
    { path: 'jobs', Component: JobsPage },
    { path: 'settings', Component: SettingsPage },
    ...Object.entries(placeholders).map(([path, [title, text]]) => ({
      path,
      element: <AdminSectionPage title={title} about={text} />,
    })),
  ],
}
