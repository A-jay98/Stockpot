import { useLocation, useNavigate } from 'react-router'
import { Page } from '../../app/Page'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../ui'
import { ImportTab } from './slots/ImportTab'
import { KitchenTab } from './slots/KitchenTab'
import { SavedTab } from './slots/SavedTab'

const tabs = ['kitchen', 'saved', 'import'] as const
type Tab = (typeof tabs)[number]

/** Recipes: From your kitchen | Saved | Import. Each tab is a slot its feature fills. */
export function RecipesPage() {
  const { pathname, search } = useLocation()
  const navigate = useNavigate()
  const fromQuery = new URLSearchParams(search).get('tab') as Tab | null
  const tab: Tab = pathname.endsWith('/import')
    ? 'import'
    : fromQuery && tabs.includes(fromQuery)
      ? fromQuery
      : 'kitchen'
  return (
    <Page title="Recipes">
      <Tabs
        value={tab}
        onValueChange={(v) =>
          navigate(v === 'kitchen' ? '/recipes' : `/recipes?tab=${v}`, { replace: true })
        }
        className="flex flex-col gap-4"
      >
        <TabsList>
          <TabsTrigger value="kitchen">From your kitchen</TabsTrigger>
          <TabsTrigger value="saved">Saved</TabsTrigger>
          <TabsTrigger value="import">Import</TabsTrigger>
        </TabsList>
        <TabsContent value="kitchen">
          <KitchenTab />
        </TabsContent>
        <TabsContent value="saved">
          <SavedTab />
        </TabsContent>
        <TabsContent value="import">
          <ImportTab />
        </TabsContent>
      </Tabs>
    </Page>
  )
}
