import { v7 as uuidv7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import { useStores } from '../catalog/hooks'
import { ManageList } from './ManageList'

const quickPicks = ['Costco', 'No Frills', 'Loblaws', 'Walmart', 'Farm Boy']

export function StoresPage() {
  const stores = useStores()
  const nextSort = (stores.at(-1)?.sort ?? 0) + 10
  const add = (name: string) =>
    void dispatch('catalog.store.create', { id: uuidv7(), name, sort: nextSort })
  return (
    <Page title="Stores">
      <ManageList
        noun="store"
        rows={stores.map((s) => ({ id: s.id, name: s.name }))}
        quickPicks={quickPicks}
        onAdd={add}
        onRename={(id, name) => void dispatch('catalog.store.update', { id, name })}
        onDelete={(id) => void dispatch('catalog.store.delete', { id })}
      />
    </Page>
  )
}
