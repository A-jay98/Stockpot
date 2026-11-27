import { Bell, BellRing } from 'lucide-react'
import { Link } from 'react-router'
import { Button, Sheet, SheetClose, SheetContent, SheetTrigger } from '../../ui'
import { type Notification, useInbox, useMarkRead } from './api'

const when = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  hour: 'numeric',
  minute: '2-digit',
})

function Entry({ n, onOpen }: { n: Notification; onOpen: () => void }) {
  return (
    <li className="rounded-lg bg-surface p-3.5 shadow-card">
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full flex-col gap-1 border-0 bg-transparent p-0 text-left text-ink"
      >
        <span className="flex items-center gap-2">
          {!n.read && (
            <span className="size-2 shrink-0 rounded-full bg-brand">
              <span className="sr-only">Unread</span>
            </span>
          )}
          <span className="flex-1 font-semibold">{n.title}</span>
          <time className="text-xs text-ink-muted" dateTime={n.createdAt}>
            {when.format(new Date(n.createdAt))}
          </time>
        </span>
        <span className="t-body-sm whitespace-pre-line text-ink-muted">{n.body}</span>
      </button>
    </li>
  )
}

/** Bell with an unread badge; opens the inbox sheet. Lives in the page header. */
export function NotificationBell() {
  const inbox = useInbox()
  const mark = useMarkRead()
  const unread = inbox.data?.unread ?? 0
  const items = inbox.data?.items ?? []
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={unread > 0 ? `Notifications, ${unread} unread` : 'Notifications'}
          className="relative"
        >
          {unread > 0 ? <BellRing aria-hidden /> : <Bell aria-hidden />}
          {unread > 0 && (
            <span
              aria-hidden
              className="absolute top-0.5 right-0.5 grid min-w-4 place-items-center rounded-full bg-brand px-1 text-[10px] leading-4 font-bold text-on-brand"
            >
              {unread > 99 ? '99+' : unread}
            </span>
          )}
        </Button>
      </SheetTrigger>
      <SheetContent
        title="Notifications"
        description={unread > 0 ? `${unread} unread` : 'You are all caught up'}
        footer={
          <>
            <Button
              variant="secondary"
              className="flex-1"
              disabled={unread === 0 || mark.isPending}
              onClick={() => mark.mutate(undefined)}
            >
              Mark all read
            </Button>
            <SheetClose asChild>
              <Button variant="ghost" asChild>
                <Link to="/settings/notifications">Settings</Link>
              </Button>
            </SheetClose>
          </>
        }
      >
        {items.length === 0 ? (
          <p className="t-body-sm m-0 text-ink-muted">
            Expiry digests will show up here, once a day, when something needs attention.
          </p>
        ) : (
          <ul className="m-0 flex list-none flex-col gap-2.5 p-0">
            {items.map((n) => (
              <Entry key={n.id} n={n} onOpen={() => !n.read && mark.mutate([n.id])} />
            ))}
          </ul>
        )}
      </SheetContent>
    </Sheet>
  )
}
