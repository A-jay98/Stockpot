import { Switch as S } from 'radix-ui'
import type { ComponentProps } from 'react'
import { cn } from './cn'

/** On/off toggle (role="switch"), styled like the design system's toggle. */
export function Switch({ className, ...props }: ComponentProps<typeof S.Root>) {
  return (
    <S.Root
      data-slot="switch"
      className={cn(
        'relative h-[26px] w-11 shrink-0 rounded-full border-0 bg-border p-0 transition-colors duration-(--duration-fast) data-[state=checked]:bg-brand disabled:opacity-50',
        className,
      )}
      {...props}
    >
      <S.Thumb className="block size-5 translate-x-[3px] rounded-full bg-white shadow-card transition-transform duration-(--duration-fast) data-[state=checked]:translate-x-[21px]" />
    </S.Root>
  )
}
