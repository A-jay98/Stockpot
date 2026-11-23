import { useId } from 'react'

// Geometry from the design export (logo/mark-*.svg): a stockpot whose three bars read as stock levels.
const SILHOUETTE = (
  <>
    <rect x="27" y="7" width="10" height="7" rx="3" />
    <rect x="9" y="13" width="46" height="7" rx="3.5" />
    <rect x="3" y="28" width="12" height="7" rx="3.5" />
    <rect x="49" y="28" width="12" height="7" rx="3.5" />
    <path d="M12 23H52V44A10 10 0 0 1 42 54H22A10 10 0 0 1 12 44Z" />
  </>
)

/** Brand mark in the current text colour (set `text-brand` on it for cobalt). */
export function Mark({ className }: { className?: string }) {
  const mask = useId()
  return (
    <svg viewBox="0 0 64 64" className={className} aria-hidden fill="currentColor">
      <defs>
        <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="64" height="64">
          <rect width="64" height="64" fill="#fff" />
          <g fill="#000">
            <rect x="18" y="30" width="28" height="4" rx="2" />
            <rect x="18" y="37" width="22" height="4" rx="2" />
            <rect x="18" y="44" width="14" height="4" rx="2" />
          </g>
        </mask>
      </defs>
      <g mask={`url(#${mask})`}>{SILHOUETTE}</g>
    </svg>
  )
}

/** Mark + wordmark. The wordmark is live text in the display face so it follows the theme. */
export function Lockup({ className }: { className?: string }) {
  return (
    <span className={className}>
      <span className="inline-flex items-center gap-2">
        <Mark className="size-8 text-brand" />
        <span className="font-display text-[22px] leading-none font-bold tracking-[-0.02em] text-ink">
          Stockpot
        </span>
      </span>
    </span>
  )
}
