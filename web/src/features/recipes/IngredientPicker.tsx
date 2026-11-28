import { Check, Unlink } from 'lucide-react'
import { useMemo, useState } from 'react'
import { dispatch } from '../../offline/outbox'
import type { Ingredient, RecipeLine } from '../../offline/replica'
import { Button, List, ListItem, Sheet, SheetContent } from '../../ui'
import { FoodIcon } from '../../ui/food-icons'
import { useIngredients } from '../catalog/hooks'
import { normalize, tokenScore, words } from '../catalog/suggest/text'
import { useIngredientIcon } from './look'
import { SearchField } from './SearchField'

const MAX_RESULTS = 12

/** Ingredients ranked against a query by their name, plural and aliases (typo tolerant). */
export function rankIngredients(all: Ingredient[], query: string, limit = MAX_RESULTS) {
  const tokens = words(normalize(query))
  if (tokens.length === 0) return []
  const scored: { ing: Ingredient; score: number }[] = []
  for (const ing of all) {
    let best = 0
    for (const name of [ing.name, ing.plural ?? '', ...ing.aliases]) {
      const ws = words(normalize(name))
      if (ws.length === 0) continue
      let sum = 0
      for (const t of tokens) sum += Math.max(0, ...ws.map((w) => tokenScore(t, w)))
      // Reward names that are about as long as the query: "garlic" over "garlic powder".
      const s = (sum / tokens.length) * (tokens.length / Math.max(tokens.length, ws.length)) ** 0.25
      if (s > best) best = s
    }
    if (best >= 0.5) scored.push({ ing, score: best })
  }
  return scored
    .sort((a, b) => b.score - a.score || a.ing.name.localeCompare(b.ing.name))
    .slice(0, limit)
    .map((x) => x.ing)
}

type Props = {
  versionId: string
  line: RecipeLine | null
  onOpenChange: (open: boolean) => void
}

/**
 * "Check match": pick the catalog ingredient an ingredient line means. Only offered on versions
 * the server has parsed (their line ids are the server's), see `canMatch`.
 */
export function IngredientPicker({ versionId, line, onOpenChange }: Props) {
  return (
    <Sheet open={line !== null} onOpenChange={onOpenChange}>
      {line ? (
        <SheetContent title="Which ingredient is this?" description={line.raw}>
          <PickerBody
            key={line.line_id}
            line={line}
            onPick={async (ingredientId) => {
              await dispatch('recipe.line.set_ingredient', {
                versionId,
                lineId: line.line_id,
                ingredientId,
              })
              onOpenChange(false)
            }}
          />
        </SheetContent>
      ) : null}
    </Sheet>
  )
}

function PickerBody({
  line,
  onPick,
}: {
  line: RecipeLine
  onPick: (ingredientId: string | null) => void
}) {
  const ingredients = useIngredients()
  const iconOf = useIngredientIcon()
  const [query, setQuery] = useState(line.name)
  const results = useMemo(() => {
    const ranked = rankIngredients(ingredients, query)
    const current = ingredients.find((i) => i.id === line.ingredient_id)
    // Keep the current match visible (first) so it reads as the default.
    return current && !ranked.some((i) => i.id === current.id) && query === line.name
      ? [current, ...ranked].slice(0, MAX_RESULTS)
      : ranked
  }, [ingredients, query, line])

  return (
    <>
      <SearchField
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        aria-label="Search ingredients"
      />
      {results.length === 0 ? (
        <p className="t-body-sm m-0 px-1 text-ink-muted">No ingredient matches “{query}”.</p>
      ) : (
        <List aria-label="Ingredients">
          {results.map((ing) => {
            const current = ing.id === line.ingredient_id
            return (
              <ListItem
                key={ing.id}
                asChild
                leading={<FoodIcon k={iconOf(ing.id)} />}
                title={ing.name}
                subtitle={
                  current
                    ? (line.match_confidence ?? 0) < CHECK_BELOW
                      ? 'Best guess · tap to confirm'
                      : 'Current match'
                    : undefined
                }
                trailing={current ? <Check className="size-5 text-brand" aria-hidden /> : null}
              >
                <button
                  type="button"
                  className="border-0 bg-transparent"
                  onClick={() => onPick(ing.id)}
                />
              </ListItem>
            )
          })}
        </List>
      )}
      {line.ingredient_id ? (
        <Button variant="ghost" onClick={() => onPick(null)}>
          <Unlink aria-hidden />
          Not a catalog ingredient
        </Button>
      ) : null}
      <p className="t-body-sm m-0 text-ink-muted">
        Matching tells Stockpot which stock this line uses, for ranking and “I cooked this”.
      </p>
    </>
  )
}

/** Confidence below which a line asks to be checked (architecture-m2 §4). */
export const CHECK_BELOW = 0.85

/** A line can be (re)matched once the server has parsed its version. */
export const canMatch = (versionRev: number, l: RecipeLine) => versionRev > 0 && l.name !== ''

/** The line wants a human look: unmatched, or matched with low confidence. */
export const needsCheck = (versionRev: number, l: RecipeLine) =>
  canMatch(versionRev, l) && !l.staple && (l.match_confidence ?? 0) < CHECK_BELOW
