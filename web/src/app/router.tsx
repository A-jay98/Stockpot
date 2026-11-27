import { createBrowserRouter, type RouteObject } from 'react-router'
import { adminRoutes } from '../admin/routes'
import { isPublicPage, setUnauthorizedHandler } from '../api/client'
import { AddPage } from '../features/add/AddPage'
import { InvitePage } from '../features/auth/InvitePage'
import { LoginPage } from '../features/auth/LoginPage'
import { PendingPage } from '../features/auth/PendingPage'
import { RequireSession } from '../features/auth/RequireSession'
import { SetupPage } from '../features/auth/SetupPage'
import { CookPage } from '../features/cook/CookPage'
import { HomePage } from '../features/home/HomePage'
import { InventoryPage } from '../features/inventory/InventoryPage'
import { ItemDetailPage } from '../features/inventory/ItemDetailPage'
import { NotificationSettingsPage } from '../features/notifications/NotificationSettingsPage'
import { RecipeEditor } from '../features/recipes/RecipeEditor'
import { RecipePage } from '../features/recipes/RecipePage'
import { RecipesPage } from '../features/recipes/RecipesPage'
import { VersionPage } from '../features/recipes/VersionPage'
import { FoodRulesPage } from '../features/settings/FoodRulesPage'
import { LocationsPage } from '../features/settings/LocationsPage'
import { SettingsPage } from '../features/settings/SettingsPage'
import { StoresPage } from '../features/settings/StoresPage'
import { ShoppingPage } from '../features/shopping/ShoppingPage'
import { IconGallery } from '../ui/food-icons/IconGallery'
import { AppShell } from './AppShell'
import { ErrorPage } from './ErrorPage'

// Dev builds only: tree-shaken out of production.
const devRoutes: RouteObject[] = import.meta.env.DEV
  ? [{ path: 'dev/icons', Component: IconGallery }]
  : []

export const routes: RouteObject[] = [
  {
    ErrorBoundary: ErrorPage,
    children: [
      ...devRoutes,
      { path: 'login', Component: LoginPage },
      { path: 'setup', Component: SetupPage },
      { path: 'invite', Component: InvitePage },
      { path: 'pending', Component: PendingPage },
      {
        ...adminRoutes,
        // Signed in first; AdminGuard inside explains when the user isn't an admin.
        element: <RequireSession>{adminRoutes.element}</RequireSession>,
      },
      {
        element: (
          <RequireSession>
            <AppShell />
          </RequireSession>
        ),
        children: [
          { index: true, Component: HomePage },
          { path: 'inventory', Component: InventoryPage },
          { path: 'inventory/:id', Component: ItemDetailPage },
          { path: 'add', Component: AddPage },
          { path: 'shopping', Component: ShoppingPage },
          { path: 'recipes', Component: RecipesPage },
          { path: 'recipes/import', Component: RecipesPage },
          { path: 'recipes/:id', Component: RecipePage },
          { path: 'recipes/:id/v/:n', Component: VersionPage },
          { path: 'recipes/:id/edit', Component: RecipeEditor },
          { path: 'recipes/:id/cook', Component: CookPage },
          { path: 'settings', Component: SettingsPage },
          { path: 'settings/stores', Component: StoresPage },
          { path: 'settings/locations', Component: LocationsPage },
          { path: 'settings/food-rules', Component: FoodRulesPage },
          { path: 'settings/notifications', Component: NotificationSettingsPage },
        ],
      },
    ],
  },
]

export function createRouter() {
  const router = createBrowserRouter(routes)
  // Session expired or missing: go to sign-in and come back afterwards.
  setUnauthorizedHandler(() => {
    const { pathname, search } = router.state.location
    if (isPublicPage(pathname)) return
    void router.navigate(`/login?next=${encodeURIComponent(pathname + search)}`, { replace: true })
  })
  return router
}
