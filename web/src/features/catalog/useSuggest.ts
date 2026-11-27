import { useMemo } from 'react'
import { useIngredients, useProductStats, useProducts, useStores } from './hooks'
import { buildIndex, type Index } from './suggest'

/** The suggest index over the replica, rebuilt only when the underlying rows change. */
export function useSuggestIndex(): Index {
  const products = useProducts()
  const ingredients = useIngredients()
  const stats = useProductStats()
  const stores = useStores()
  return useMemo(
    () => buildIndex({ products, ingredients, stats, stores }),
    [products, ingredients, stats, stores],
  )
}
