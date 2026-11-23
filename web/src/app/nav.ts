import { ChefHat, House, type LucideIcon, Plus, Refrigerator, ShoppingBasket } from 'lucide-react'

export type NavItem = { to: string; label: string; icon: LucideIcon; end?: boolean }

/** Primary destinations. Order matches the phone tab bar; Add is the centre action. */
export const primaryNav = {
  home: { to: '/', label: 'Home', icon: House, end: true },
  inventory: { to: '/inventory', label: 'Inventory', icon: Refrigerator },
  add: { to: '/add', label: 'Quick add', icon: Plus },
  shopping: { to: '/shopping', label: 'Shopping', icon: ShoppingBasket },
  recipes: { to: '/recipes', label: 'Recipes', icon: ChefHat },
} satisfies Record<string, NavItem>

export const tabOrder: NavItem[] = [
  primaryNav.home,
  primaryNav.inventory,
  primaryNav.add,
  primaryNav.shopping,
  primaryNav.recipes,
]
