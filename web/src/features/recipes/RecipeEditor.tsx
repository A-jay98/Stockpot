import { Page, Placeholder } from '../../app/Page'

/** Create and edit recipes (each save is a new version). Built by m2-3. */
export function RecipeEditor() {
  return (
    <Page title="Edit recipe">
      <Placeholder milestone="M2">
        Write or edit a recipe; every save keeps the old version.
      </Placeholder>
    </Page>
  )
}
