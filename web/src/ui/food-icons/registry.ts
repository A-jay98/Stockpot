import raw from './registry.json'

export type IconEntry = { set: 'fe' | 'tb'; name: string; label: string; tags: string[] }

/** Closed list of icon keys: seeds, the LLM enricher and users may only pick from here. */
export const iconRegistry = raw as Record<string, IconEntry>

export const FALLBACK_ICON = 'basket'

export function isIconKey(key: string | null | undefined): key is string {
  return !!key && Object.hasOwn(iconRegistry, key)
}

/** Category icons are registered as `cat-<slug>`. */
export function categoryIconKey(slug: string): string {
  return `cat-${slug}`
}

type IconSource = string | null | undefined

/**
 * Icon resolution: product → ingredient → ancestors (nearest first) → category → basket.
 * Unknown keys (e.g. a registry entry retired later) are skipped, never rendered broken.
 */
export function resolveFoodIcon(chain: {
  product?: IconSource
  ingredient?: IconSource
  ancestors?: IconSource[]
  category?: IconSource
}): string {
  const candidates = [chain.product, chain.ingredient, ...(chain.ancestors ?? []), chain.category]
  return candidates.find(isIconKey) ?? FALLBACK_ICON
}
