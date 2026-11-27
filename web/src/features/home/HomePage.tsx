import { Bell, UserRound } from 'lucide-react'
import { Link } from 'react-router'
import { AccountSheet } from '../../app/AccountSheet'
import { Page } from '../../app/Page'
import { ServerStatus } from '../../app/ServerStatus'
import { Button, cn, List } from '../../ui'
import { today as todayOf } from '../inventory/dates'
import { useStock } from '../inventory/hooks'
import { StockListItem } from '../inventory/StockListItem'
import { freshness, isLow } from '../inventory/status'

const toneClass = {
  soon: 'bg-soon-bg text-soon',
  expired: 'bg-expired-bg text-expired',
  low: 'bg-low-bg text-low',
}

export function HomePage() {
  const stock = useStock()
  const now = todayOf()
  const soon = stock.filter((r) => freshness(r.item, now) === 'soon')
  const expired = stock.filter((r) => freshness(r.item, now) === 'expired')
  const low = stock.filter((r) => isLow(r.item))
  // Expired first, then by date: what to eat first.
  const useFirst = [...expired, ...soon].sort((a, b) =>
    (a.item.expiresOn ?? '').localeCompare(b.item.expiresOn ?? ''),
  )
  const stats = [
    { tone: 'soon', label: 'Within 3 days', n: soon.length, to: '/inventory?show=soon' },
    { tone: 'expired', label: 'Expired', n: expired.length, to: '/inventory?show=expired' },
    { tone: 'low', label: 'Running low', n: low.length, to: '/inventory?show=low' },
  ] as const

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
            <b className="tabular font-display text-[30px] leading-[34px]">{s.n}</b>
            <span className="text-[13px] font-semibold">{s.label}</span>
          </Link>
        ))}
      </div>
      {useFirst.length > 0 ? (
        <section aria-label="Eat first" className="flex flex-col gap-2">
          <h2 className="t-label m-0 text-ink-muted">Eat first</h2>
          <List>
            {useFirst.slice(0, 8).map((r) => (
              <StockListItem key={r.item.id} row={r} now={now} />
            ))}
          </List>
          {useFirst.length > 8 && (
            <Link to="/inventory?show=soon" className="text-sm font-semibold text-brand">
              See all {useFirst.length}
            </Link>
          )}
        </section>
      ) : (
        <p className="t-body-sm m-0 rounded-lg bg-surface p-4 text-ink-muted shadow-card">
          {stock.length === 0
            ? 'Nothing in the kitchen yet. Add what you have from Quick add.'
            : 'Nothing is close to expiring.'}
        </p>
      )}
      <ServerStatus />
    </Page>
  )
}
