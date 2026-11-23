import { Bell, UserRound } from 'lucide-react'
import { Link } from 'react-router'
import { AccountSheet } from '../../app/AccountSheet'
import { Page, Placeholder } from '../../app/Page'
import { ServerStatus } from '../../app/ServerStatus'
import { Button, cn } from '../../ui'

const stats = [
  { tone: 'soon', label: 'Within 3 days', to: '/inventory' },
  { tone: 'expired', label: 'Expired', to: '/inventory' },
  { tone: 'low', label: 'Running low', to: '/shopping' },
] as const

const toneClass = {
  soon: 'bg-soon-bg text-soon',
  expired: 'bg-expired-bg text-expired',
  low: 'bg-low-bg text-low',
}

export function HomePage() {
  return (
    <Page
      title="Use soon"
      actions={
        <>
          <Button
            variant="secondary"
            size="icon"
            aria-label="Alerts"
            className="bg-surface shadow-card"
          >
            <Bell aria-hidden />
          </Button>
          <AccountSheet
            trigger={
              <button
                type="button"
                aria-label="Account"
                className="grid size-9 place-items-center rounded-full border-0 bg-brand-soft text-[13px] font-bold text-brand md:hidden"
              >
                <UserRound className="size-5" aria-hidden />
              </button>
            }
          />
        </>
      }
    >
      <div className="grid grid-cols-3 gap-2">
        {stats.map((s) => (
          <Link
            key={s.label}
            to={s.to}
            className={cn('flex flex-col gap-0.5 rounded-lg p-3 no-underline', toneClass[s.tone])}
          >
            <b className="tabular font-display text-[30px] leading-[34px]">0</b>
            <span className="text-[13px] font-semibold">{s.label}</span>
          </Link>
        ))}
      </div>
      <Placeholder milestone="M1">
        What to eat first, sorted by expiry with leftovers included, plus a “cook tonight” pick.
      </Placeholder>
      <ServerStatus />
    </Page>
  )
}
