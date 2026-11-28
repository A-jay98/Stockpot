import {
  type PointerEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  useRef,
  useState,
} from 'react'
import { cn } from './cn'

export type SwipeAction = {
  label: string
  icon: ReactNode
  /** Token class for the action's background, e.g. `bg-brand text-on-brand`. */
  tone: string
  onAction: () => void
}

type Props = {
  children: ReactNode
  /** Revealed by swiping right; letting go past the threshold runs it. */
  start?: SwipeAction
  /** Revealed by swiping left; they stay open as buttons. A long swipe runs the last one. */
  end?: SwipeAction[]
  /** Open state is owned by the list so only one row is open at a time. */
  open: boolean
  onOpenChange: (open: boolean) => void
  className?: string
}

const ACTION_W = 76
const DECIDE = 8 // px before a drag counts as horizontal or vertical

/**
 * A list row with swipe actions (phones): right to run `start` (check off), left to reveal `end`
 * (e.g. +1, delete). Vertical drags scroll as usual; every action is also reachable without
 * swiping, so this is a shortcut, not the only way.
 */
export function SwipeRow({ children, start, end = [], open, onOpenChange, className }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const drag = useRef<{
    x: number
    y: number
    base: number
    dir?: 'h' | 'v'
    id: number
    /** Live offset: pointerup can arrive before the last move re-renders. */
    at?: number
  } | null>(null)
  const moved = useRef(false)
  const [dx, setDx] = useState<number | null>(null)
  const openW = end.length * ACTION_W
  const offset = dx ?? (open ? -openW : 0)

  const width = () => ref.current?.offsetWidth ?? 360

  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    drag.current = { x: e.clientX, y: e.clientY, base: open ? -openW : 0, id: e.pointerId }
    moved.current = false
  }
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d || d.id !== e.pointerId) return
    const mx = e.clientX - d.x
    const my = e.clientY - d.y
    if (!d.dir) {
      if (Math.abs(mx) < DECIDE && Math.abs(my) < DECIDE) return
      d.dir = Math.abs(mx) > Math.abs(my) ? 'h' : 'v'
      if (d.dir === 'h') e.currentTarget.setPointerCapture?.(e.pointerId)
    }
    if (d.dir !== 'h') return
    moved.current = true
    let x = d.base + mx
    if (!start) x = Math.min(x, 0)
    if (!end.length) x = Math.max(x, 0)
    d.at = x
    setDx(x)
  }
  const up = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    drag.current = null
    if (!d || d.id !== e.pointerId || d.dir !== 'h' || d.at === undefined) {
      setDx(null)
      return
    }
    const w = width()
    const x = d.at
    setDx(null)
    if (start && x > Math.min(110, w * 0.3)) {
      onOpenChange(false)
      start.onAction()
    } else if (end.length && x < -Math.max(w * 0.6, openW + 80)) {
      onOpenChange(false)
      end.at(-1)?.onAction()
    } else onOpenChange(end.length > 0 && x < -ACTION_W / 2)
  }
  // A drag is not a tap on what's underneath; a tap on an open row closes it.
  const click = (e: ReactMouseEvent) => {
    if (moved.current || open) {
      e.preventDefault()
      e.stopPropagation()
      moved.current = false
      if (open) onOpenChange(false)
    }
  }

  const run = (a: SwipeAction) => () => {
    onOpenChange(false)
    a.onAction()
  }
  const dragging = dx !== null

  return (
    <div ref={ref} className={cn('relative overflow-hidden', className)}>
      {start && offset > 0 && (
        <div
          aria-hidden
          className={cn('absolute inset-0 flex items-center gap-2 pl-5 font-semibold', start.tone)}
        >
          {start.icon}
          {start.label}
        </div>
      )}
      {end.length > 0 && (
        <div
          className={cn('absolute inset-y-0 right-0 flex', !open && offset >= 0 && 'invisible')}
          aria-hidden={!open}
        >
          {end.map((a) => (
            <button
              key={a.label}
              type="button"
              tabIndex={open ? 0 : -1}
              onClick={run(a)}
              className={cn(
                'flex flex-col items-center justify-center gap-0.5 border-0 p-0 text-[12px] font-semibold',
                a.tone,
              )}
              style={{ width: ACTION_W }}
            >
              {a.icon}
              {a.label}
            </button>
          ))}
        </div>
      )}
      <div
        data-swipe-offset={Math.round(offset)}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
        onClickCapture={click}
        className={cn(
          'relative select-none bg-surface',
          !dragging && 'transition-transform duration-200',
        )}
        style={{ transform: `translateX(${offset}px)`, touchAction: 'pan-y' }}
      >
        {children}
      </div>
    </div>
  )
}
