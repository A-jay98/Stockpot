import { Keyboard, PencilLine, Receipt, ScanBarcode } from 'lucide-react'
import { Page, Placeholder } from '../../app/Page'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../ui'
import { AddItemForm } from '../inventory/AddItemForm'

const ways = [
  {
    id: 'type',
    label: 'Type',
    icon: Keyboard,
    text: 'Free text or voice: “2L milk, dozen eggs, chicken use by friday”.',
  },
  {
    id: 'scan',
    label: 'Scan',
    icon: ScanBarcode,
    text: 'Barcode scan with an Open Food Facts lookup.',
  },
  {
    id: 'receipt',
    label: 'Receipt',
    icon: Receipt,
    text: 'Receipt photo, read on the server and mapped to products.',
  },
]

export function AddPage() {
  return (
    <Page title="Quick add">
      <Tabs defaultValue="manual" className="flex flex-col gap-4">
        <TabsList aria-label="How to add">
          <TabsTrigger value="manual">
            <PencilLine className="size-4" aria-hidden />
            Manual
          </TabsTrigger>
          {ways.map(({ id, label, icon: Icon }) => (
            <TabsTrigger key={id} value={id}>
              <Icon className="size-4" aria-hidden />
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="manual">
          <AddItemForm />
        </TabsContent>
        {ways.map((w) => (
          <TabsContent key={w.id} value={w.id}>
            <Placeholder milestone="M2">{w.text}</Placeholder>
          </TabsContent>
        ))}
      </Tabs>
    </Page>
  )
}
