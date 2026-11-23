import { Tabs as T } from 'radix-ui'
import type { ComponentProps } from 'react'
import { cn } from './cn'

export const Tabs = T.Root
export const TabsContent = T.Content

/** Segmented control look (mockups: Fridge / Freezer / Pantry). */
export function TabsList({ className, ...props }: ComponentProps<typeof T.List>) {
  return (
    <T.List
      className={cn(
        'grid auto-cols-fr grid-flow-col gap-0.5 rounded-md bg-surface-2 p-[3px]',
        className,
      )}
      {...props}
    />
  )
}

export function TabsTrigger({ className, ...props }: ComponentProps<typeof T.Trigger>) {
  return (
    <T.Trigger
      className={cn(
        'flex items-center justify-center gap-1.5 rounded-[8px] px-1 py-2 text-[14px] font-semibold text-ink-muted',
        'data-[state=active]:bg-surface data-[state=active]:text-ink data-[state=active]:shadow-card',
        className,
      )}
      {...props}
    />
  )
}
