import { Dialog as D } from 'radix-ui'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from './cn'

export const Sheet = D.Root
export const SheetTrigger = D.Trigger
export const SheetClose = D.Close

type ContentProps = ComponentProps<typeof D.Content> & {
  title: ReactNode
  description?: ReactNode
  /** Pinned action area under the scrolling body (e.g. the primary button). */
  footer?: ReactNode
}

/** Bottom sheet on phones, centered panel from the `md` breakpoint up. */
export function SheetContent({
  title,
  description,
  footer,
  className,
  children,
  ...props
}: ContentProps) {
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-40 bg-scrim animate-fade-in" />
      <D.Content
        className={cn(
          'fixed inset-x-0 bottom-0 z-50 flex max-h-[92dvh] flex-col overflow-hidden rounded-t-xl bg-bg shadow-sheet animate-sheet-in',
          'md:inset-auto md:top-1/2 md:left-1/2 md:max-h-[88dvh] md:w-[560px] md:-translate-x-1/2 md:-translate-y-1/2 md:rounded-xl md:animate-dialog-in',
          className,
        )}
        {...(description ? {} : { 'aria-describedby': undefined })}
        {...props}
      >
        <div
          aria-hidden
          className="mx-auto mt-2 mb-1 h-[5px] w-10 shrink-0 rounded-full bg-border md:hidden"
        />
        <div className="flex flex-col gap-1 px-4 pt-2 pb-3 md:px-6 md:pt-5">
          <D.Title className="t-title-lg m-0">{title}</D.Title>
          {description && (
            <D.Description className="t-body-sm m-0 text-ink-muted">{description}</D.Description>
          )}
        </div>
        <div className="flex flex-1 flex-col gap-4 overflow-auto px-4 pb-6 md:px-6">{children}</div>
        {footer && (
          <div className="flex shrink-0 gap-2.5 border-t border-border bg-bg px-4 pt-3 pb-[max(12px,env(safe-area-inset-bottom))] md:px-6">
            {footer}
          </div>
        )}
      </D.Content>
    </D.Portal>
  )
}
