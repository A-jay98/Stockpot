import { ToggleGroup as G } from 'radix-ui'
import type { ReactNode } from 'react'
import { cn } from './cn'

type Option<T extends string> = { value: T; label: ReactNode }

type SegmentedProps<T extends string> = {
  value: T
  onValueChange: (value: T) => void
  options: Option<T>[]
  'aria-label': string
  className?: string
  disabled?: boolean
}

/** Single-choice setting (radio semantics) with the same look as TabsList. */
export function Segmented<T extends string>({
  value,
  onValueChange,
  options,
  className,
  disabled,
  ...aria
}: SegmentedProps<T>) {
  return (
    <G.Root
      type="single"
      value={value}
      disabled={disabled}
      // Radix allows deselecting the active item; a setting always has a value.
      onValueChange={(v) => v && onValueChange(v as T)}
      className={cn(
        'grid auto-cols-fr grid-flow-col gap-0.5 rounded-md bg-surface-2 p-[3px]',
        className,
      )}
      {...aria}
    >
      {options.map((o) => (
        <G.Item
          key={o.value}
          value={o.value}
          className="flex items-center justify-center gap-1.5 rounded-[8px] px-1 py-2 text-[14px] font-semibold text-ink-muted data-[state=on]:bg-surface data-[state=on]:text-ink data-[state=on]:shadow-card disabled:opacity-50"
        >
          {o.label}
        </G.Item>
      ))}
    </G.Root>
  )
}
