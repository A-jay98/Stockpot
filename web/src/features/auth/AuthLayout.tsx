import { type ReactNode, useEffect } from 'react'
import { Lockup } from '../../app/Logo'

/** Centered single-column layout for the signed-out screens. */
export function AuthLayout({ title, children }: { title: string; children: ReactNode }) {
  useEffect(() => {
    document.title = `${title} · Stockpot`
  }, [title])
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-[420px] flex-col justify-center gap-6 px-4 py-8">
      <Lockup />
      <h1 className="t-title-lg m-0">{title}</h1>
      {children}
    </main>
  )
}

/** Inline error under a form, announced to screen readers. */
export function FormError({ message }: { message?: string }) {
  if (!message) return null
  return (
    <p role="alert" className="t-body-sm m-0 text-expired">
      {message}
    </p>
  )
}
