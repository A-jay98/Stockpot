import { cva, type VariantProps } from 'class-variance-authority'
import { Slot } from 'radix-ui'
import type { ComponentProps } from 'react'
import { cn } from './cn'

export const buttonVariants = cva(
  'inline-flex items-center justify-center gap-2 font-semibold whitespace-nowrap transition-colors duration-(--duration-fast) disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-5 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        primary: 'bg-brand text-on-brand hover:bg-brand-strong',
        secondary: 'bg-surface-2 text-ink hover:bg-border',
        ghost: 'bg-transparent text-ink hover:bg-surface-2',
        danger: 'bg-expired-bg text-expired hover:opacity-90',
        link: 'bg-transparent p-0 text-brand hover:underline',
      },
      size: {
        sm: 'h-8 rounded-[8px] px-3 text-[13px] [&_svg]:size-4',
        md: 'h-11 rounded-md px-4 text-[15px]',
        lg: 'h-12 rounded-md px-5 text-base',
        icon: 'size-10 rounded-full',
      },
      block: { true: 'w-full' },
    },
    compoundVariants: [{ variant: 'link', className: 'h-auto px-0' }],
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)

export type ButtonProps = ComponentProps<'button'> &
  VariantProps<typeof buttonVariants> & {
    /** Render the child element (e.g. a router Link) with button styles. */
    asChild?: boolean
  }

export function Button({ className, variant, size, block, asChild, ...props }: ButtonProps) {
  const Comp = asChild ? Slot.Root : 'button'
  return (
    <Comp
      data-slot="button"
      className={cn(buttonVariants({ variant, size, block }), className)}
      {...(asChild ? {} : { type: props.type ?? 'button' })}
      {...props}
    />
  )
}
