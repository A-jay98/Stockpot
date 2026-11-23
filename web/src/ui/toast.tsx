import { CircleAlert, CircleCheck, Info, X } from 'lucide-react'
import { Toast as T } from 'radix-ui'
import { type ReactNode, useSyncExternalStore } from 'react'
import { cn } from './cn'

export type ToastTone = 'info' | 'success' | 'error'

export type ToastOptions = {
  title: ReactNode
  description?: ReactNode
  tone?: ToastTone
  /** Milliseconds; Infinity keeps it until dismissed. */
  duration?: number
  action?: { label: string; onClick: () => void }
}

type Entry = ToastOptions & { id: number; open: boolean }

// A tiny external store so any module (including non-React code such as the API
// client) can raise a toast without prop drilling.
let entries: Entry[] = []
let nextId = 1
const listeners = new Set<() => void>()
const emit = () => {
  for (const l of listeners) l()
}

export function toast(options: ToastOptions): number {
  const id = nextId++
  entries = [...entries, { ...options, id, open: true }]
  emit()
  return id
}

export function dismissToast(id: number) {
  entries = entries.map((e) => (e.id === id ? { ...e, open: false } : e))
  emit()
}

function remove(id: number) {
  entries = entries.filter((e) => e.id !== id)
  emit()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

const icons: Record<ToastTone, ReactNode> = {
  info: <Info />,
  success: <CircleCheck />,
  error: <CircleAlert className="text-expired" />,
}

/** Mount once near the root. Bottom on phones (above the tab bar), bottom-right on desktop. */
export function Toaster() {
  const list = useSyncExternalStore(
    subscribe,
    () => entries,
    () => entries,
  )
  return (
    <T.Provider swipeDirection="down">
      {list.map((e) => (
        <T.Root
          key={e.id}
          open={e.open}
          duration={e.duration ?? 5000}
          type={e.tone === 'error' ? 'foreground' : 'background'}
          onOpenChange={(open) => {
            if (!open) {
              dismissToast(e.id)
              // Leave time for the exit state before unmounting.
              setTimeout(() => remove(e.id), 200)
            }
          }}
          className={cn(
            'flex items-center gap-2.5 rounded-md bg-ink px-3.5 py-3 text-[14px] font-semibold text-bg shadow-sheet animate-fade-in',
            'data-[state=closed]:opacity-0 [&_svg]:size-5 [&_svg]:shrink-0',
          )}
        >
          {icons[e.tone ?? 'info']}
          <div className="flex min-w-0 flex-1 flex-col">
            <T.Title>{e.title}</T.Title>
            {e.description && (
              <T.Description className="font-normal opacity-80">{e.description}</T.Description>
            )}
          </div>
          {e.action && (
            <T.Action altText={e.action.label} asChild>
              <button
                type="button"
                className="font-bold underline underline-offset-2"
                onClick={e.action.onClick}
              >
                {e.action.label}
              </button>
            </T.Action>
          )}
          <T.Close aria-label="Dismiss" className="-mr-1 grid size-7 place-items-center opacity-70">
            <X className="size-4!" />
          </T.Close>
        </T.Root>
      ))}
      <T.Viewport className="fixed inset-x-4 bottom-[calc(96px+env(safe-area-inset-bottom))] z-[60] m-0 flex list-none flex-col gap-2 p-0 outline-none md:inset-x-auto md:right-8 md:bottom-8 md:w-[380px]" />
    </T.Provider>
  )
}
