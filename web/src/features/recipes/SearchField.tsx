import { Search } from 'lucide-react'
import type { ComponentProps } from 'react'
import { Input } from '../../ui'

/** Search input with a leading magnifier; pass `aria-label`. */
export function SearchField(props: ComponentProps<'input'> & { 'aria-label': string }) {
  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-surface px-3 text-ink-muted focus-within:border-brand">
      <Search className="size-5 shrink-0" aria-hidden />
      <Input
        type="search"
        {...props}
        className="border-0 bg-transparent px-0 focus-visible:outline-0"
      />
    </div>
  )
}
