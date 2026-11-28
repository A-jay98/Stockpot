import {
  ArrowLeft,
  BookOpen,
  Cable,
  Carrot,
  Cpu,
  DatabaseBackup,
  Flag,
  Gauge,
  ListChecks,
  type LucideIcon,
  MessageSquareWarning,
  Ruler,
  ScrollText,
  ShieldCheck,
  Timer,
  Users,
} from 'lucide-react'
import { NavLink, Outlet } from 'react-router'
import { Lockup } from '../app/Logo'

type Section = { to: string; label: string; icon: LucideIcon; end?: boolean }

/** Admin sections (architecture §8). */
export const adminSections: Section[] = [
  { to: '/admin', label: 'Overview', icon: Gauge, end: true },
  { to: '/admin/users', label: 'Users', icon: Users },
  { to: '/admin/audit', label: 'Audit log', icon: ShieldCheck },
  { to: '/admin/access', label: 'Access log', icon: Cable },
  { to: '/admin/logs', label: 'System logs', icon: ScrollText },
  { to: '/admin/jobs', label: 'Jobs', icon: ListChecks },
  { to: '/admin/settings', label: 'Settings & flags', icon: Flag },
  { to: '/admin/llm', label: 'LLM', icon: Cpu },
  { to: '/admin/backups', label: 'Backups', icon: DatabaseBackup },
  { to: '/admin/food-rules', label: 'Food rules', icon: Timer },
  { to: '/admin/catalog', label: 'Catalog', icon: Carrot },
  { to: '/admin/recipes', label: 'Recipes', icon: BookOpen },
  { to: '/admin/reports', label: 'Reports', icon: MessageSquareWarning },
  { to: '/admin/defaults', label: 'Defaults', icon: Ruler },
]

const link =
  'flex shrink-0 items-center gap-2.5 rounded-md px-2.5 py-[9px] font-semibold whitespace-nowrap text-ink-muted no-underline hover:bg-surface-2 aria-[current=page]:bg-brand-soft aria-[current=page]:text-brand [&_svg]:size-5'

/** Desktop-first: side nav from `md`, a scrolling section strip on phones. */
export function AdminLayout() {
  return (
    <div className="flex min-h-dvh flex-col md:grid md:grid-cols-[232px_minmax(0,1fr)]">
      <nav
        aria-label="Admin"
        className="flex gap-1 overflow-x-auto border-b border-border bg-surface px-3 py-2 md:sticky md:top-0 md:h-dvh md:flex-col md:gap-0.5 md:border-r md:border-b-0 md:py-[18px]"
      >
        <Lockup className="mx-2 mb-[18px] hidden md:block" />
        {adminSections.map(({ to, label, icon: Icon, end }) => (
          <NavLink key={to} to={to} end={end} className={link}>
            <Icon aria-hidden />
            {label}
          </NavLink>
        ))}
        <div className="hidden flex-1 md:block" />
        <NavLink to="/" className={link}>
          <ArrowLeft aria-hidden />
          Back to app
        </NavLink>
      </nav>
      <main className="flex min-w-0 flex-col">
        <Outlet />
      </main>
    </div>
  )
}
