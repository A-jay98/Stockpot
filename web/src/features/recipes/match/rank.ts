import type {
  Ingredient,
  Product,
  Recipe,
  RecipeLine,
  RecipeVersion,
  StockItem,
  Substitution,
} from '../../../offline/replica'
import { daysBetween } from '../../inventory/dates'

/**
 * "From your kitchen" ranking (architecture-m2 §8), pure and client-side over the replica so it is
 * instant and works offline. Matching uses M1's "is a kind of" semantics: a line asking for
 * mandarin is satisfied by clementines in stock, never the other way round.
 */

/** Slugs assumed in every kitchen when no line says otherwise (server default of recipes.assumed_staples). */
export const DEFAULT_STAPLES = ['water', 'salt', 'black-pepper', 'ice']

/** Weight of a line covered by a swap, relative to one you have. */
export const SUB_WEIGHT = 0.6
/** Cook tonight needs at least this share of required lines in stock. */
export const COOK_TONIGHT_HAVE = 0.7
/** Urgency at or above this counts as "urgent" (within 3 days). */
export const URGENT = 0.6

/** How urgently a stock item should be used: 1 expired/today/tomorrow, 0.6 ≤ 3 days, 0.3 ≤ 7 days. */
export function urgency(expiresOn: string | null, today: string): number {
  if (!expiresOn) return 0
  const d = daysBetween(today, expiresOn)
  if (d <= 1) return 1
  if (d <= 3) return 0.6
  if (d <= 7) return 0.3
  return 0
}

/** A live stock item that can serve a line. */
export type StockUse = {
  item: StockItem
  product: Product
  days: number | null
  urgency: number
}

export type LineStatus = 'have' | 'sub' | 'missing' | 'skip'

export type LineMatch = {
  line: RecipeLine
  /** skip: optional or a staple; never counts. Unmatched required lines are missing. */
  status: LineStatus
  /** The stock that has it (have), soonest first. */
  stock: StockUse[]
  /** The swap that covers it (sub). */
  sub?: Substitution
  /** The stock the swap would use (sub). */
  subStock: StockUse[]
}

export type RankedRecipe = {
  recipe: Recipe
  version: RecipeVersion
  lines: LineMatch[]
  required: number
  have: number
  sub: number
  missing: number
  score: number
  /** Expiring stock this recipe would use, most urgent first, one per item. */
  soon: StockUse[]
  /** The first line covered by a swap, for the card's "No X? Use Y" line. */
  firstSub?: LineMatch
}

/** Everything the ranking reads from the replica. */
export type KitchenData = {
  today: string
  ingredients: Ingredient[]
  products: Product[]
  stock: StockItem[]
  substitutions: Substitution[]
  /** Household staple slugs; default DEFAULT_STAPLES. Lines also carry the server's staple flag. */
  stapleSlugs?: string[]
}

/** The kitchen indexed once for ranking many recipes. */
export type Kitchen = {
  today: string
  ingredients: Map<string, Ingredient>
  /** ingredient id → stock that satisfies it (the ingredient or any kind of it). */
  stockFor: Map<string, StockUse[]>
  /** Ingredient ids that are staples (or kinds of one). */
  staples: Set<string>
  /** ingredient id → swaps for it, recipe-specific first, then household, then seed. */
  subsFor: Map<string, Substitution[]>
}

/** A live stock item: some amount left. */
export const isLive = (s: StockItem): boolean =>
  s.amountKind === 'portions'
    ? (s.portions ?? 0) > 0
    : s.amountKind === 'exact'
      ? s.quantity === null || Number(s.quantity) > 0
      : true

/** The ingredient and its ancestors (bounded, cycle-safe). */
function lineage(id: string, ingredients: Map<string, Ingredient>): string[] {
  const out = [id]
  const seen = new Set(out)
  for (let p = ingredients.get(id)?.parentId; p && !seen.has(p) && out.length < 16; ) {
    out.push(p)
    seen.add(p)
    p = ingredients.get(p)?.parentId
  }
  return out
}

const subRank = (s: Substitution) => (s.recipeId ? 0 : s.householdId ? 1 : 2)

export function indexKitchen(d: KitchenData): Kitchen {
  const ingredients = new Map(d.ingredients.map((i) => [i.id, i]))
  const products = new Map(d.products.map((p) => [p.id, p]))
  const stockFor = new Map<string, StockUse[]>()
  for (const item of d.stock) {
    const product = products.get(item.productId)
    if (!product || !isLive(item)) continue
    const use: StockUse = {
      item,
      product,
      days: item.expiresOn ? daysBetween(d.today, item.expiresOn) : null,
      urgency: urgency(item.expiresOn, d.today),
    }
    const satisfied = new Set<string>()
    for (const own of [product.ingredientId, ...product.extraIngredientIds])
      if (own) for (const a of lineage(own, ingredients)) satisfied.add(a)
    for (const a of satisfied) stockFor.set(a, [...(stockFor.get(a) ?? []), use])
  }
  for (const uses of stockFor.values())
    uses.sort((a, b) => (a.days ?? Infinity) - (b.days ?? Infinity))

  const slugs = new Set(d.stapleSlugs ?? DEFAULT_STAPLES)
  const staples = new Set<string>()
  for (const i of ingredients.values())
    if (lineage(i.id, ingredients).some((a) => slugs.has(ingredients.get(a)?.slug ?? '')))
      staples.add(i.id)

  const subsFor = new Map<string, Substitution[]>()
  for (const s of d.substitutions)
    subsFor.set(s.ingredientId, [...(subsFor.get(s.ingredientId) ?? []), s])
  for (const list of subsFor.values()) list.sort((a, b) => subRank(a) - subRank(b))
  return { today: d.today, ingredients, stockFor, staples, subsFor }
}

/** Whether a swap works for this recipe with what is in the kitchen; returns the stock it uses. */
function subWorks(k: Kitchen, s: Substitution, recipeId: string): StockUse[] | null {
  if (s.recipeId && s.recipeId !== recipeId) return null
  if (s.uses.length === 0) return null
  const used: StockUse[] = []
  for (const u of s.uses) {
    const stock = k.stockFor.get(u.ingredient_id)
    if (stock?.length) used.push(stock[0] as StockUse)
    else if (!k.staples.has(u.ingredient_id)) return null
  }
  return used
}

/** Where one line stands in this kitchen. */
export function matchLine(k: Kitchen, line: RecipeLine, recipeId: string): LineMatch {
  const id = line.ingredient_id
  if (line.optional || line.staple || (id && k.staples.has(id)))
    return { line, status: 'skip', stock: [], subStock: [] }
  if (!id) return { line, status: 'missing', stock: [], subStock: [] }
  const stock = k.stockFor.get(id) ?? []
  if (stock.length) return { line, status: 'have', stock, subStock: [] }
  // A swap for the line's ingredient or a broader one ("shallot" swaps cover banana shallots).
  for (const a of lineage(id, k.ingredients))
    for (const s of k.subsFor.get(a) ?? []) {
      const used = subWorks(k, s, recipeId)
      if (used) return { line, status: 'sub', stock: [], sub: s, subStock: used }
    }
  return { line, status: 'missing', stock: [], subStock: [] }
}

/** Rank one recipe version against the kitchen. */
export function rankRecipe(k: Kitchen, recipe: Recipe, version: RecipeVersion): RankedRecipe {
  const lines = version.ingredients.map((l) => matchLine(k, l, recipe.id))
  let have = 0
  let sub = 0
  let missing = 0
  let urgencySum = 0
  const soon = new Map<string, StockUse>()
  for (const m of lines) {
    if (m.status === 'skip') continue
    if (m.status === 'missing') {
      missing++
      continue
    }
    if (m.status === 'have') have++
    else sub++
    // The line would use its most urgent stock first.
    const uses = m.status === 'have' ? m.stock : m.subStock
    let best: StockUse | undefined
    for (const u of uses) if (!best || u.urgency > best.urgency) best = u
    if (best && best.urgency > 0) {
      urgencySum += best.urgency
      soon.set(best.item.id, best)
    }
  }
  const required = have + sub + missing
  const coverage = required === 0 ? 0 : (have + SUB_WEIGHT * sub) / required
  const score =
    coverage * (1 + 0.5 * urgencySum) * (1 + 0.1 * Math.log(1 + Math.max(0, recipe.cookedCount)))
  return {
    recipe,
    version,
    lines,
    required,
    have,
    sub,
    missing,
    score,
    soon: [...soon.values()].sort((a, b) => b.urgency - a.urgency || (a.days ?? 0) - (b.days ?? 0)),
    firstSub: lines.find((m) => m.status === 'sub'),
  }
}

/** Recipes the kitchen tab ranks: saved ones, and ideas that haven't expired. */
export function isCandidate(r: Recipe, now: string): boolean {
  if (r.saved) return true
  return r.sourceKind === 'idea' && (!r.ideaExpiresAt || r.ideaExpiresAt > now)
}

/** Rank recipes: best score first; ties put recently cooked ones last (variety), then by title. */
export function rankRecipes(
  k: Kitchen,
  recipes: Recipe[],
  versions: Map<string, RecipeVersion>,
): RankedRecipe[] {
  const out: RankedRecipe[] = []
  for (const r of recipes) {
    const v = r.currentVersionId ? versions.get(r.currentVersionId) : undefined
    if (v) out.push(rankRecipe(k, r, v))
  }
  return out.sort(
    (a, b) =>
      b.score - a.score ||
      (a.recipe.lastCookedAt ?? '').localeCompare(b.recipe.lastCookedAt ?? '') ||
      a.recipe.title.localeCompare(b.recipe.title),
  )
}

/** Home's "Cook tonight": the top recipe with ≥ 70% in stock that uses something urgent. */
export function cookTonight(ranked: RankedRecipe[]): RankedRecipe | undefined {
  return ranked.find(
    (r) =>
      r.required > 0 &&
      r.have / r.required >= COOK_TONIGHT_HAVE &&
      r.soon.some((s) => s.urgency >= URGENT),
  )
}

/** "200 g", "1.5": a decimal string scaled by factor, trimmed to 2 places. */
export function scaleQty(qty: string | undefined, factor: number): string | undefined {
  if (qty === undefined) return undefined
  const n = Number(qty)
  if (!Number.isFinite(n)) return qty
  return String(Math.round(n * factor * 100) / 100)
}
