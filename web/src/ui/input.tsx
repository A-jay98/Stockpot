import { type ComponentProps, type ReactNode, useId } from 'react'
import { cn } from './cn'

const fieldClass =
  'w-full rounded-md border border-border bg-surface px-3 py-[11px] text-base text-ink placeholder:text-ink-muted outline-none focus-visible:border-brand focus-visible:outline-2 focus-visible:outline-offset-0 disabled:opacity-50 aria-invalid:border-expired'

export function Input({ className, ...props }: ComponentProps<'input'>) {
  return <input data-slot="input" className={cn(fieldClass, className)} {...props} />
}

export function Textarea({ className, ...props }: ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(fieldClass, 'min-h-24 resize-none', className)}
      {...props}
    />
  )
}

type FieldProps = {
  label: string
  hint?: string
  error?: string
  children: (props: { id: string; 'aria-describedby'?: string; 'aria-invalid'?: true }) => ReactNode
}

/** Label + control + hint/error, wired for accessibility. */
export function Field({ label, hint, error, children }: FieldProps) {
  const id = useId()
  const describedBy = error || hint ? `${id}-desc` : undefined
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-[13px] font-semibold text-ink-muted">
        {label}
      </label>
      {children({
        id,
        'aria-describedby': describedBy,
        ...(error ? { 'aria-invalid': true as const } : {}),
      })}
      {(error || hint) && (
        <span id={describedBy} className={cn('text-xs', error ? 'text-expired' : 'text-ink-muted')}>
          {error ?? hint}
        </span>
      )}
    </div>
  )
}
