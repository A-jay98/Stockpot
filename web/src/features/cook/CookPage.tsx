import { Page, Placeholder } from '../../app/Page'

/** "I cooked this" sheet: deduct stock, store leftovers. Built by m2-6. */
export function CookPage() {
  return (
    <Page title="I cooked this">
      <Placeholder milestone="M2">
        Portions made, what you used from stock, and leftovers for the fridge or freezer.
      </Placeholder>
    </Page>
  )
}
