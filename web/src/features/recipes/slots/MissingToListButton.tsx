import { useLiveQuery } from 'dexie-react-hooks'
import { ListPlus } from 'lucide-react'
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { v7 as uuidv7 } from 'uuid'
import { db } from '../../../offline/db'
import { dispatch } from '../../../offline/outbox'
import { Button, toast } from '../../../ui'
import '../../shopping/commands'
import { missingItems } from '../match/missing'
import { useRecipeMatch } from '../match/useKitchen'

/**
 * Adds the recipe's missing ingredients to the first shopping list (creating one if there is none),
 * scaled to `servings` when given, each linked to the household's product for that ingredient.
 */
export function MissingToListButton({
  recipeId,
  versionId,
  servings,
}: {
  recipeId: string
  versionId: string
  servings?: number
}) {
  const match = useRecipeMatch(recipeId, versionId)
  const products = useLiveQuery(() => db.product.toArray(), [], [])
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  if (!match) return null
  const { kitchen, ranked } = match
  if (ranked.missing === 0)
    return (
      <Button variant="secondary" disabled>
        <ListPlus aria-hidden />
        All in stock
      </Button>
    )
  const base = ranked.version.servings
  const factor = servings && base ? servings / base : 1

  const add = async () => {
    setBusy(true)
    try {
      let list = (await db.shopping_list.orderBy('sort').toArray())[0]
      if (!list) {
        const id = uuidv7()
        await dispatch('shopping.list.create', { id, name: 'Shopping', sort: 10 })
        list = await db.shopping_list.get(id)
      }
      if (!list) return
      const [stats, open] = await Promise.all([
        db.product_stat.toArray(),
        db.shopping_item.where('listId').equals(list.id).toArray(),
      ])
      const items = missingItems(kitchen, ranked, products, stats, open, factor)
      for (const it of items)
        await dispatch('shopping.item.add', {
          id: uuidv7(),
          listId: list.id,
          recipeId,
          text: it.text,
          ...(it.productId ? { productId: it.productId } : {}),
          ...(it.qty && it.unit ? { qty: it.qty, unit: it.unit } : {}),
        })
      const listName = list.name
      toast(
        items.length
          ? {
              title: `Added ${items.length} to ${listName}`,
              tone: 'success',
              action: { label: 'Open list', onClick: () => navigate('/shopping') },
            }
          : { title: `Already on ${listName}`, description: 'Everything missing is on the list.' },
      )
    } finally {
      setBusy(false)
    }
  }
  return (
    <Button variant="secondary" disabled={busy} onClick={() => void add()}>
      <ListPlus aria-hidden />
      Missing to list ({ranked.missing})
    </Button>
  )
}
