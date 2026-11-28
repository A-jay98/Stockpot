import { useMemo } from 'react'
import type { Category, Ingredient } from '../../offline/replica'
import { categoryIconKey, resolveFoodIcon } from '../../ui/food-icons'
import { useCategories, useIngredients } from './hooks'

/** Lookups for the icon chain, built once per replica change. */
export type IconLookup = {
  ingredients: Map<string, Ingredient>
  categoryIcons: Map<string, string>
}

export function iconLookup(ingredients: Ingredient[], categories: Category[]): IconLookup {
  return {
    ingredients: new Map(ingredients.map((i) => [i.id, i])),
    categoryIcons: new Map(categories.map((c) => [c.id, c.icon ?? categoryIconKey(c.slug)])),
  }
}

export function useIconLookup(): IconLookup {
  const ingredients = useIngredients()
  const categories = useCategories()
  return useMemo(() => iconLookup(ingredients, categories), [ingredients, categories])
}

/**
 * The food icon for a product or catalog pick: its own icon, else its ingredient's, else the
 * nearest ancestor's, else its category's, else the basket.
 */
export function foodIconFor(
  src: { icon?: string | null; ingredientId?: string | null; categoryId?: string | null },
  { ingredients, categoryIcons }: IconLookup,
): string {
  const ingredient = src.ingredientId ? ingredients.get(src.ingredientId) : undefined
  const ancestors: (string | null)[] = []
  for (let a = ingredient, n = 0; a?.parentId && n < 12; n++) {
    a = ingredients.get(a.parentId)
    if (a) ancestors.push(a.icon)
  }
  const categoryId = src.categoryId ?? ingredient?.categoryId
  return resolveFoodIcon({
    product: src.icon,
    ingredient: ingredient?.icon,
    ancestors,
    category: categoryId ? categoryIcons.get(categoryId) : undefined,
  })
}
