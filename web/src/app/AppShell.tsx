import { Plus, Shield, UserRound } from 'lucide-react'
import { NavLink, Outlet } from 'react-router'
import { useMe } from '../features/auth/session'
import { RejectedSheet } from '../offline/RejectedSheet'
import { useReplicaSync } from '../offline/useReplicaSync'
import { OfflineIndicator } from '../pwa/OfflineIndicator'
import { Button, cn } from '../ui'
import { AccountSheet } from './AccountSheet'
import { Lockup } from './Logo'
import { primaryNav, tabOrder } from './nav'

const sideLink =
  'flex items-center gap-2.5 rounded-md px-2.5 py-[9px] font-semibold text-ink-muted no-underline hover:bg-surface-2 aria-[current=page]:bg-brand-soft aria-[current=page]:text-brand [&_svg]:size-5'

/** Phones: content + bottom tab bar. From `md` up: side nav + content. */
export function AppShell() {
  const me = useMe().data
  const isAdmin = me?.user.systemRole === 'admin'
  useReplicaSync(me?.activeHouseholdId ?? undefined)
  return (
    <div className="flex min-h-dvh flex-col md:grid md:grid-cols-[232px_minmax(0,1fr)]">
      <nav
        aria-label="Main"
        className="sticky top-0 hidden h-dvh flex-col gap-0.5 border-r border-border bg-surface px-3 py-[18px] md:flex"
      >
        <Lockup className="mx-2 mb-[18px]" />
        <Button asChild className="mb-3.5 justify-start">
          <NavLink to={primaryNav.add.to}>
            <Plus aria-hidden />
            Quick add
          </NavLink>
        </Button>
        {tabOrder
          .filter((t) => t !== primaryNav.add)
          .map(({ to, label, icon: Icon, end }) => (
            <NavLink key={to} to={to} end={end} className={sideLink}>
              <Icon aria-hidden />
              {label}
            </NavLink>
          ))}
        <div className="flex-1" />
        {isAdmin && (
          <NavLink to="/admin" className={sideLink}>
            <Shield aria-hidden />
            Admin
          </NavLink>
        )}
        <AccountSheet
          trigger={
            <button type="button" className={cn(sideLink, 'border-0 bg-transparent text-left')}>
              <UserRound aria-hidden />
              Account
            </button>
          }
        />
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="sticky top-0 z-30 pt-[env(safe-area-inset-top)]">
          <OfflineIndicator />
          <RejectedSheet />
        </div>
        <main className="flex flex-1 flex-col pb-[calc(84px+env(safe-area-inset-bottom))] md:pb-0">
          <Outlet />
        </main>
      </div>

      <nav
        aria-label="Tabs"
        className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-5 items-end border-t border-border bg-surface px-2 pt-2 pb-[max(10px,env(safe-area-inset-bottom))] md:hidden"
      >
        {tabOrder.map(({ to, label, icon: Icon, end }) =>
          to === primaryNav.add.to ? (
            <NavLink
              key={to}
              to={to}
              aria-label={label}
              className="group flex flex-col items-center py-1 no-underline"
            >
              <span className="-mt-[26px] grid size-[52px] place-items-center rounded-full bg-brand text-on-brand shadow-sheet ring-brand-soft ring-offset-2 ring-offset-surface group-aria-[current=page]:ring-4 [&_svg]:size-[26px]">
                <Icon aria-hidden />
              </span>
            </NavLink>
          ) : (
            <NavLink
              key={to}
              to={to}
              end={end}
              className="flex flex-col items-center gap-[3px] py-1 text-[11px] font-semibold text-ink-muted no-underline aria-[current=page]:text-brand [&_svg]:size-5"
            >
              <Icon aria-hidden />
              {label}
            </NavLink>
          ),
        )}
      </nav>
    </div>
  )
}
