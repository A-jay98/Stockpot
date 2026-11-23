import { X } from 'lucide-react'
import { Dialog as D } from 'radix-ui'
import type { ComponentProps, ReactNode } from 'react'
import { Button } from './button'
import { cn } from './cn'

export const Dialog = D.Root
export const DialogTrigger = D.Trigger
export const DialogClose = D.Close

type ContentProps = ComponentProps<typeof D.Content> & {
  title: ReactNode
  description?: ReactNode
}

/** Centered modal dialog. For bottom sheets on mobile use Sheet. */
export function DialogContent({ title, description, className, children, ...props }: ContentProps) {
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-40 bg-scrim animate-fade-in" />
      <D.Content
        className={cn(
          'fixed top-1/2 left-1/2 z-50 flex max-h-[85dvh] w-[calc(100%-32px)] max-w-[480px] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-auto rounded-xl bg-surface p-5 shadow-sheet animate-dialog-in',
          className,
        )}
        {...(description ? {} : { 'aria-describedby': undefined })}
        {...props}
      >
        <div className="flex items-start gap-3">
          <D.Title className="t-title m-0 flex-1">{title}</D.Title>
          <D.Close asChild>
            <Button variant="ghost" size="icon" aria-label="Close" className="-m-2">
              <X />
            </Button>
          </D.Close>
        </div>
        {description && (
          <D.Description className="t-body-sm m-0 text-ink-muted">{description}</D.Description>
        )}
        {children}
      </D.Content>
    </D.Portal>
  )
}
