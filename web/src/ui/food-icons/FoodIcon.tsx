import type { CSSProperties } from 'react'
import { useColourfulIcons } from './preference'
import { FALLBACK_ICON, iconRegistry, isIconKey } from './registry'
import flatSprite from './sprites/flat.svg'
import highContrastSprite from './sprites/high-contrast.svg'

export type FoodIconStyle = 'high-contrast' | 'flat'

type Props = {
  /** Registry key, e.g. `tangerine` or `tb-tofu`. Unknown keys render the basket. */
  k: string | null | undefined
  size?: number
  /** Overrides the user preference (gallery). */
  variant?: FoodIconStyle
  /** Accessible name. Omit for decorative use next to a text label. */
  label?: string
  className?: string
}

/** Sprite `<use>`: high contrast follows `currentColor`; flat keeps its own colours. */
export function FoodIcon({ k, size = 22, variant, label, className }: Props) {
  const [colourful] = useColourfulIcons()
  const key = isIconKey(k) ? k : FALLBACK_ICON
  const sprite =
    (variant ?? (colourful ? 'flat' : 'high-contrast')) === 'flat' ? flatSprite : highContrastSprite
  const style: CSSProperties = { width: size, height: size, flexShrink: 0 }
  return (
    <svg
      className={className}
      style={style}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      data-icon={key}
    >
      <use href={`${sprite}#fi-${key}`} />
    </svg>
  )
}

export function iconLabel(key: string): string {
  return iconRegistry[key]?.label ?? key
}
