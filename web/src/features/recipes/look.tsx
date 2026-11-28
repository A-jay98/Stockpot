import { ChefHat } from 'lucide-react'
import { useMemo } from 'react'
import type { ThumbWidth } from '../../lib/photos'
import type { Ingredient, RecipeVersion } from '../../offline/replica'
import { cn } from '../../ui'
import { categoryIconKey, FoodIcon, isIconKey } from '../../ui/food-icons'
import { useCategories, useIngredients } from '../catalog/hooks'
import { BlobImage } from '../photos/BlobImage'

// Tile colours for recipes without a cover photo: deep enough for a white icon in both themes.
const TINTS = [
  '#B0571F',
  '#7A3B2E',
  '#2D7A6A',
  '#2344A6',
  '#6A3D9E',
  '#1D7443',
  '#8F5400',
  '#9E3B55',
]

/** Stable tile colour for a recipe id. */
export function recipeTint(id: string): string {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0
  return TINTS[Math.abs(h) % TINTS.length] as string
}

export type IngredientIndex = Map<string, Ingredient>

export function useIngredientIndex(): IngredientIndex {
  const ingredients = useIngredients()
  return useMemo(() => new Map(ingredients.map((i) => [i.id, i])), [ingredients])
}

/** Food icon for one ingredient: its own, then its ancestors', then its category's. */
export function useIngredientIcon(): (ingredientId: string | undefined) => string | null {
  const index = useIngredientIndex()
  const categories = useCategories()
  return useMemo(() => {
    const catIcon = new Map(categories.map((c) => [c.id, c.icon ?? categoryIconKey(c.slug)]))
    return (id) => {
      let ing = id ? index.get(id) : undefined
      const category = ing?.categoryId
      for (let depth = 0; ing && depth < 8; depth++) {
        if (isIconKey(ing.icon)) return ing.icon
        ing = ing.parentId ? index.get(ing.parentId) : undefined
      }
      const c = category ? catIcon.get(category) : undefined
      return isIconKey(c) ? c : null
    }
  }, [index, categories])
}

/** The icon that stands for a recipe: its first matched, non-staple ingredient. */
export function useRecipeIcon(version: RecipeVersion | null | undefined): string | null {
  const iconOf = useIngredientIcon()
  if (!version) return null
  for (const l of version.ingredients) {
    if (l.staple || l.optional || !l.ingredient_id) continue
    const k = iconOf(l.ingredient_id)
    if (k) return k
  }
  return null
}

/** Coloured tile standing in for a cover photo. */
export function RecipeTile({
  recipeId,
  icon,
  size,
  className,
}: {
  recipeId: string
  icon: string | null
  size: number
  className?: string
}) {
  return (
    <span
      aria-hidden
      className={cn('grid shrink-0 place-items-center text-white', className)}
      style={{ background: recipeTint(recipeId) }}
    >
      {icon ? (
        <FoodIcon k={icon} size={size} variant="high-contrast" />
      ) : (
        <ChefHat style={{ width: size, height: size }} className="opacity-85" />
      )}
    </span>
  )
}

/** The recipe's cover photo, or its coloured tile when it has none. */
export function RecipeCover({
  recipeId,
  coverBlob,
  icon,
  size,
  w,
  alt,
  className,
}: {
  recipeId: string
  coverBlob: string | null
  icon: string | null
  size: number
  w: ThumbWidth
  alt: string
  className?: string
}) {
  if (!coverBlob)
    return <RecipeTile recipeId={recipeId} icon={icon} size={size} className={className} />
  return (
    <BlobImage
      blobRef={coverBlob}
      w={w}
      alt={alt}
      className={cn('shrink-0 object-cover', className)}
      style={{ background: recipeTint(recipeId) }}
    />
  )
}
