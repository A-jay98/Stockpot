import { useState } from 'react'
import { v7 as uuidv7 } from 'uuid'
import { Page } from '../../app/Page'
import { dispatch } from '../../offline/outbox'
import type { LocationKind } from '../../offline/replica'
import { Segmented } from '../../ui'
import { useLocations } from '../catalog/hooks'
import { ManageList } from './ManageList'

const kinds: { value: LocationKind; label: string }[] = [
  { value: 'fridge', label: 'Fridge' },
  { value: 'freezer', label: 'Freezer' },
  { value: 'pantry', label: 'Pantry' },
  { value: 'other', label: 'Other' },
]

export function LocationsPage() {
  const locations = useLocations()
  const [kind, setKind] = useState<LocationKind>('fridge')
  const nextSort = (locations.at(-1)?.sort ?? 0) + 10
  return (
    <Page title="Locations">
      <ManageList
        noun="location"
        rows={locations.map((l) => ({
          id: l.id,
          name: l.name,
          subtitle: kinds.find((k) => k.value === l.kind)?.label,
        }))}
        addExtra={
          <Segmented<LocationKind>
            aria-label="Kind"
            value={kind}
            onValueChange={setKind}
            options={kinds}
          />
        }
        onAdd={(name) =>
          void dispatch('catalog.location.create', { id: uuidv7(), name, kind, sort: nextSort })
        }
        onRename={(id, name) => void dispatch('catalog.location.update', { id, name })}
        onDelete={(id) => void dispatch('catalog.location.delete', { id })}
      />
    </Page>
  )
}
