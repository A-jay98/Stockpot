import type { Ingredient, Product } from '../../offline/replica'
import { normalize } from './suggest/text'

/** Rule and LLM matches below this need the user's OK (resolve.AutoAccept on the server). */
export const MATCH_CONFIDENT = 0.85

/**
 * What a product is matched to, for showing next to it:
 * - `matched`: the user picked it, or the resolver/LLM is confident;
 * - `checking`: the background LLM hasn't looked yet (`ingredient` is the rule's guess, if any);
 * - `confirm`: the LLM (or a failed/skipped LLM pass) left a low-confidence guess, so ask;
 * - `unmatched`: nothing found, or the user chose no match (not shown on the list).
 */
export type MatchState =
  | { kind: 'matched'; ingredient: Ingredient; extras: Ingredient[] }
  | { kind: 'confirm'; ingredient: Ingredient; extras: Ingredient[] }
  | { kind: 'checking'; ingredient?: Ingredient; extras: Ingredient[] }
  | { kind: 'unmatched' }

export function matchState(p: Product, ingredients: Map<string, Ingredient>): MatchState {
  const ingredient = p.ingredientId ? ingredients.get(p.ingredientId) : undefined
  const extras = p.extraIngredientIds.flatMap((id) => {
    const i = ingredients.get(id)
    return i ? [i] : []
  })
  if (!ingredient)
    // The user saying "no match" is settled, whatever the enricher state.
    return p.enrichState === 'pending' && p.matchSource !== 'user'
      ? { kind: 'checking', extras }
      : { kind: 'unmatched' }
  if (p.matchSource === 'user' || p.matchSource === 'barcode')
    return { kind: 'matched', ingredient, extras }
  if ((p.matchConfidence ?? 0) >= MATCH_CONFIDENT) return { kind: 'matched', ingredient, extras }
  if (p.enrichState === 'pending') return { kind: 'checking', ingredient, extras }
  return { kind: 'confirm', ingredient, extras }
}

/**
 * Whether to show the match under the name: not when it only repeats the name ("Milk" matched to
 * Milk, settled or still being checked); always when the user is asked to confirm it.
 */
export function matchWorthShowing(p: Product, m: MatchState): boolean {
  if (m.kind === 'confirm' || m.kind === 'unmatched' || !m.ingredient || m.extras.length)
    return true
  const name = normalize(p.name)
  return ![m.ingredient.name, m.ingredient.plural].some((n) => n && normalize(n) === name)
}
