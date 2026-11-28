import type { RouteObject } from 'react-router'
import { AccessPage } from './AccessPage'
import { AdminGuard } from './AdminGuard'
import { AdminLayout } from './AdminLayout'
import { AuditPage } from './AuditPage'
import { BackupsPage } from './BackupsPage'
import { CatalogPage } from './CatalogPage'
import { DefaultsPage } from './DefaultsPage'
import { FoodRulesPage } from './FoodRulesPage'
import { JobsPage } from './JobsPage'
import { LLMPage } from './LLMPage'
import { LogsPage } from './LogsPage'
import { OverviewPage } from './OverviewPage'
import { RecipesPage } from './RecipesPage'
import { ReportsPage } from './ReportsPage'
import { SettingsPage } from './SettingsPage'
import { UsersPage } from './UsersPage'

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
    { path: 'llm', Component: LLMPage },
    { path: 'backups', Component: BackupsPage },
    { path: 'food-rules', Component: FoodRulesPage },
    { path: 'catalog', Component: CatalogPage },
    { path: 'recipes', Component: RecipesPage },
    { path: 'reports', Component: ReportsPage },
    { path: 'defaults', Component: DefaultsPage },
  ],
}
