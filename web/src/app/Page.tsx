import type { ReactNode } from 'react'
import { useEffect } from 'react'

type PageProps = {
  title: string
  /** Buttons on the right of the title bar. */
  actions?: ReactNode
  children?: ReactNode
}

/** Title bar + scrolling content column shared by every screen. */
export function Page({ title, actions, children }: PageProps) {
  useEffect(() => {
    document.title = title === 'Stockpot' ? title : `${title} · Stockpot`
  }, [title])
  return (
    <>
      <header className="flex items-center gap-2.5 px-4 pt-3 pb-2.5 md:px-8 md:pt-[22px] md:pb-3">
        <h1 className="t-title-lg m-0 min-w-0 flex-1 text-[26px] leading-8">{title}</h1>
        {actions}
      </header>
      <div className="flex flex-col gap-4 px-4 pb-6 md:px-8 md:pb-8">{children}</div>
    </>
  )
}

/** Stand-in body for screens built in later milestones. */
export function Placeholder({ milestone, children }: { milestone: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg bg-surface p-4 shadow-card">
      <span className="t-label text-brand">Coming in {milestone}</span>
      <p className="t-body-sm m-0 text-ink-muted">{children}</p>
    </div>
  )
}
