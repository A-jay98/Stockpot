import { expect, test } from 'vitest'
import type {
  Ingredient,
  Product,
  Recipe,
  RecipeLine,
  RecipeVersion,
} from '../../../offline/replica'
import { missingItems } from './missing'
import { indexKitchen, rankRecipe } from './rank'

const ing = (id: string, parentId: string | null = null) =>
  ({ id, slug: id, parentId }) as Ingredient
const prod = (id: string, name: string, ingredientId: string) =>
  ({ id, name, ingredientId, extraIngredientIds: [] as string[] }) as Product
const line = (id: string, name: string, ingredient_id?: string, more = {}): RecipeLine => ({
  line_id: id,
  raw: name,
  name,
  optional: false,
  staple: false,
  ...(ingredient_id ? { ingredient_id } : {}),
  ...more,
})

test('lines for the same product, ingredient or name add one item', () => {
  const k = indexKitchen({
    today: '2026-11-28',
    ingredients: [ing('onion'), ing('red-onion', 'onion'), ing('garlic')],
    products: [prod('p-onion', 'Red onions', 'red-onion')],
    stock: [],
    substitutions: [],
  })
  const version = {
    ingredients: [
      line('a', 'onion', 'onion', { qty: '1', unit: 'ea' }),
      line('b', 'onion, for garnish', 'onion', { qty: '2', unit: 'ea' }),
      line('c', 'garlic', 'garlic'),
      line('d', 'garlic cloves', 'garlic'),
      line('e', 'Saffron'),
      line('f', 'saffron '),
      line('g', 'cilantro', undefined, { qty: '1', unit: 'handful' }),
    ],
  } as RecipeVersion
  const r = rankRecipe(k, { id: 'r', cookedCount: 0 } as Recipe, version)
  const items = missingItems(k, r, [prod('p-onion', 'Red onions', 'red-onion')], [], [])
  expect(items).toEqual([
    { lineId: 'a', text: 'Red onions', productId: 'p-onion', qty: '1', unit: 'ea' },
    { lineId: 'c', text: 'garlic', ingredientId: 'garlic' },
    { lineId: 'e', text: 'Saffron' },
    // "handful" isn't a list unit: no bare quantity.
    { lineId: 'g', text: 'cilantro' },
  ])
})
