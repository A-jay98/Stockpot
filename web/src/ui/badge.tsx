import { cva, type VariantProps } from 'class-variance-authority'
import { ArrowDown, Clock, TriangleAlert } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from './cn'

export const badgeVariants = cva(
  'inline-flex items-center gap-[5px] rounded-sm px-2 py-[3px] text-[12.5px] leading-[18px] font-semibold whitespace-nowrap [&_svg]:size-3.5 [&_svg]:shrink-0 [&_svg]:stroke-[2.4]',
  {
    variants: {
      tone: {
        fresh: 'bg-fresh-bg text-fresh',
        soon: 'bg-soon-bg text-soon',
        expired: 'bg-expired-bg text-expired',
        low: 'bg-low-bg text-low',
        neutral: 'bg-surface-2 text-ink-muted',
        brand: 'bg-brand-soft text-brand',
      },
    },
    defaultVariants: { tone: 'neutral' },
  },
)

export type BadgeProps = ComponentProps<'span'> & VariantProps<typeof badgeVariants>

export function Badge({ className, tone, ...props }: BadgeProps) {
  return <span data-slot="badge" className={cn(badgeVariants({ tone }), className)} {...props} />
}

export type FoodState = 'fresh' | 'soon' | 'expired' | 'low'

// Every food state is colour + shape + text, never colour alone (design rule).
const stateIcon: Record<FoodState, ReactNode> = {
  fresh: <span aria-hidden className="size-[7px] rounded-full bg-current" />,
  soon: <Clock aria-hidden />,
  expired: <TriangleAlert aria-hidden />,
  low: <ArrowDown aria-hidden />,
}

const defaultLabel: Record<FoodState, string> = {
  fresh: 'Fresh',
  soon: 'Use soon',
  expired: 'Expired',
  low: 'Low',
}

/** Food state chip: fresh / soon / expired / low. `children` overrides the label (e.g. "2 days"). */
export function StateBadge({
  state,
  children,
  ...props
}: Omit<BadgeProps, 'tone'> & { state: FoodState }) {
  return (
    <Badge tone={state} data-state={state} {...props}>
      {stateIcon[state]}
      {children ?? defaultLabel[state]}
    </Badge>
  )
}
