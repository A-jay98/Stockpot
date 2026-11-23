import type { RouteObject } from 'react-router'
import { AdminLayout } from './AdminLayout'
import { AdminSectionPage } from './AdminSectionPage'

const about: Record<string, [string, string]> = {
  users: ['Users', 'Pending approvals, invites, users, roles and sessions.'],
  audit: ['Audit log', 'Every security event and change, filterable by user, action and time.'],
  logs: ['System logs', 'Server logs with level and text filters, live tail.'],
  jobs: ['Jobs', 'Background jobs: list, retry, cancel.'],
  settings: ['Settings & flags', 'Typed runtime settings and feature flags with change history.'],
  llm: ['LLM', 'Providers, keys, models, daily caps and usage.'],
}

/** `/admin` route group. Admin-only guarding arrives with identity (m0-2). */
export const adminRoutes: RouteObject = {
  path: 'admin',
  Component: AdminLayout,
  children: [
    {
      index: true,
      element: (
        <AdminSectionPage
          title="Overview"
          about="Version, uptime, database, jobs, errors and LLM usage at a glance."
        />
      ),
    },
    ...Object.entries(about).map(([path, [title, text]]) => ({
      path,
      element: <AdminSectionPage title={title} about={text} />,
    })),
  ],
}
