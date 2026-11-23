import { Slot } from 'radix-ui'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from './cn'

/** Card-styled list with hairline dividers between rows. */
export function List({ className, ...props }: ComponentProps<'ul'>) {
  return (
    <ul
      data-slot="list"
      className={cn(
        'm-0 list-none rounded-lg bg-surface p-0 py-1 shadow-card [&>li+li]:border-t [&>li+li]:border-border',
        className,
      )}
      {...props}
    />
  )
}

type ListItemProps = Omit<ComponentProps<'div'>, 'title'> & {
  leading?: ReactNode
  title: ReactNode
  subtitle?: ReactNode
  trailing?: ReactNode
  /**
   * Render the row as the given child element (e.g. `<Link to="…" />`, no children of its own)
   * so the whole row is interactive.
   */
  asChild?: boolean
  children?: ReactNode
}

export function ListItem({
  leading,
  title,
  subtitle,
  trailing,
  asChild,
  className,
  children,
  ...props
}: ListItemProps) {
  const Comp = asChild ? Slot.Root : 'div'
  const content = (
    <>
      {leading && (
        <span className="grid size-[38px] shrink-0 place-items-center rounded-[10px] bg-surface-2 text-ink-muted [&_svg]:size-[19px]">
          {leading}
        </span>
      )}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate font-semibold">{title}</span>
        {subtitle && <span className="text-[13px] leading-5 text-ink-muted">{subtitle}</span>}
      </span>
      {trailing && <span className="flex flex-col items-end gap-1">{trailing}</span>}
    </>
  )
  return (
    <li>
      <Comp
        data-slot="list-item"
        className={cn(
          'flex w-full items-center gap-3 px-4 py-[11px] text-left text-ink no-underline',
          asChild && 'hover:bg-surface-2',
          className,
        )}
        {...props}
      >
        {asChild && <Slot.Slottable>{children}</Slot.Slottable>}
        {content}
      </Comp>
    </li>
  )
}
