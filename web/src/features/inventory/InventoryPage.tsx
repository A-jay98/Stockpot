import { Search } from 'lucide-react'
import { Page, Placeholder } from '../../app/Page'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../ui'

const locations = [
  { id: 'fridge', name: 'Fridge' },
  { id: 'freezer', name: 'Freezer' },
  { id: 'pantry', name: 'Pantry' },
]

export function InventoryPage() {
  return (
    <Page title="Inventory">
      <label className="flex items-center gap-2 rounded-md border border-border bg-surface px-3 py-[9px] text-ink-muted [&_svg]:size-5">
        <Search aria-hidden />
        <input
          disabled
          placeholder="Search items"
          aria-label="Search"
          className="min-w-0 flex-1 border-0 bg-transparent text-base outline-none"
        />
      </label>
      <Tabs defaultValue="fridge" className="flex flex-col gap-4">
        <TabsList aria-label="Location">
          {locations.map((l) => (
            <TabsTrigger key={l.id} value={l.id}>
              {l.name}
            </TabsTrigger>
          ))}
        </TabsList>
        {locations.map((l) => (
          <TabsContent key={l.id} value={l.id}>
            <Placeholder milestone="M1">
              Everything in the {l.name.toLowerCase()} with amounts and expiry, sorted by what to
              use first.
            </Placeholder>
          </TabsContent>
        ))}
      </Tabs>
    </Page>
  )
}
