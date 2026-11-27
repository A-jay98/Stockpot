import { TriangleAlert } from 'lucide-react'
import { Button, Sheet, SheetContent, SheetTrigger } from '../ui'
import { discardRejected, parseProblem, retryRejected } from './sync'
import { useRejectedCommands } from './useReplicaSync'

const titles: Record<string, string> = {
  'catalog.product.create': 'Add product',
  'catalog.product.update': 'Edit product',
  'catalog.product.delete': 'Delete product',
  'catalog.product.set_ingredient': 'Change what a product matches',
  'catalog.store.create': 'Add store',
  'catalog.store.update': 'Edit store',
  'catalog.store.delete': 'Delete store',
  'catalog.location.create': 'Add location',
  'catalog.location.update': 'Edit location',
  'catalog.location.delete': 'Delete location',
  'catalog.ingredient.create': 'Add ingredient',
}

const reasons: Record<string, string> = {
  gone: 'It was changed or deleted on another device.',
  invalid: "The server didn't accept it.",
  forbidden: "You can't do this.",
}

/** Banner + sheet listing changes the server refused, with Retry and Discard. */
export function RejectedSheet() {
  const rejected = useRejectedCommands()
  if (rejected.length === 0) return null
  return (
    <Sheet>
      <SheetTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center justify-center gap-2 border-0 bg-expired-bg px-4 py-1.5 text-[13px] font-semibold text-expired [&_svg]:size-4"
        >
          <TriangleAlert aria-hidden />
          {rejected.length === 1
            ? "1 change couldn't sync"
            : `${rejected.length} changes couldn't sync`}
        </button>
      </SheetTrigger>
      <SheetContent
        title="Couldn't sync"
        description="The server refused these changes. Retry them or discard them."
      >
        <ul className="m-0 flex list-none flex-col gap-3 p-0">
          {rejected.map((r) => {
            const problem = parseProblem(r.error)
            return (
              <li key={r.seq} className="flex flex-col gap-2 rounded-lg bg-surface p-3 shadow-card">
                <span className="font-semibold">{titles[r.type] ?? r.type}</span>
                <span className="t-body-sm text-ink-muted">
                  {reasons[problem.code] ?? 'Rejected.'} {problem.detail}
                </span>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void retryRejected(r.seq as number)}
                  >
                    Retry
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => void discardRejected(r.seq as number)}
                  >
                    Discard
                  </Button>
                </div>
              </li>
            )
          })}
        </ul>
      </SheetContent>
    </Sheet>
  )
}
