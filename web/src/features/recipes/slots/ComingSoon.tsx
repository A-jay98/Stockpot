import type { ReactNode } from 'react'

/** The placeholder a slot shows until its feature lands. */
export function ComingSoon({ children }: { children: ReactNode }) {
  return (
    <p className="t-body-sm m-0 rounded-lg bg-surface p-4 text-ink-muted shadow-card">{children}</p>
  )
}
