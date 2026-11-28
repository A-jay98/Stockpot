import type { ComponentProps } from 'react'
import { cn } from '../../ui'
import { storeLook } from './brands'

type StoreLike = { name: string; color: string | null }

/** A store's round badge: its logo glyph, else its monogram, on the brand colour. */
export function StoreBadge({
  store,
  size = 18,
  ringed = false,
}: {
  store: StoreLike
  size?: number
  /** Outline it, for a badge on its own brand colour. */
  ringed?: boolean
}) {
  const look = storeLook(store)
  return (
    <span
      aria-hidden
      data-brand={look.brand}
      className="inline-grid shrink-0 place-items-center overflow-hidden rounded-full font-bold leading-none"
      style={{
        width: size,
        height: size,
        background: look.bg,
        color: look.fg,
        fontSize: Math.round(size * (look.mono.length > 2 ? 0.34 : 0.46)),
        boxShadow: ringed ? `0 0 0 1.5px ${look.fg}` : undefined,
      }}
    >
      {look.path ? (
        <svg
          aria-hidden="true"
          viewBox="0 0 24 24"
          width={size * 0.66}
          height={size * 0.66}
          fill="currentColor"
        >
          <path d={look.path} />
        </svg>
      ) : (
        look.mono
      )}
    </span>
  )
}

type ChipProps = Omit<ComponentProps<'button'>, 'type'> & { store: StoreLike; pressed: boolean }

/** A toggle chip themed after the store: its badge, and its brand colour when on. */
export function StoreChip({ store, pressed, className, style, ...rest }: ChipProps) {
  const look = storeLook(store)
  return (
    <button
      type="button"
      aria-pressed={pressed}
      className={cn(
        'flex items-center gap-1.5 rounded-full border border-border bg-surface py-1 pr-3 pl-1 text-[13px] font-semibold text-ink',
        className,
      )}
      style={
        pressed ? { ...style, background: look.bg, borderColor: look.bg, color: look.fg } : style
      }
      {...rest}
    >
      <StoreBadge store={store} size={22} ringed={pressed} />
      {store.name}
    </button>
  )
}
