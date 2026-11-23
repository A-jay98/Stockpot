import { createBrowserRouter, type RouteObject } from 'react-router'
import { adminRoutes } from '../admin/routes'
import { setUnauthorizedHandler } from '../api/client'
import { AddPage } from '../features/add/AddPage'
import { LoginPage } from '../features/auth/LoginPage'
import { HomePage } from '../features/home/HomePage'
import { InventoryPage } from '../features/inventory/InventoryPage'
import { RecipesPage } from '../features/recipes/RecipesPage'
import { ShoppingPage } from '../features/shopping/ShoppingPage'
import { AppShell } from './AppShell'
import { ErrorPage } from './ErrorPage'

export const routes: RouteObject[] = [
  {
    ErrorBoundary: ErrorPage,
    children: [
      { path: 'login', Component: LoginPage },
      adminRoutes,
      {
        Component: AppShell,
        children: [
          { index: true, Component: HomePage },
          { path: 'inventory', Component: InventoryPage },
          { path: 'add', Component: AddPage },
          { path: 'shopping', Component: ShoppingPage },
          { path: 'recipes', Component: RecipesPage },
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
    if (pathname === '/login') return
    void router.navigate(`/login?next=${encodeURIComponent(pathname + search)}`, { replace: true })
  })
  return router
}
