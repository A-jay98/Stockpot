import { useCategories, useIngredients } from '../features/catalog/hooks'
import { summary } from '../features/settings/FoodRulesPage'
import { useAdminFoodRules } from './foodrules-api'
import { AdminPage, ErrorNote, Table } from './ui'

/** Read-only view of the shelf-life rules: Stockpot's defaults and every household's overrides. */
export function FoodRulesPage() {
  const { data, error } = useAdminFoodRules()
  const categories = useCategories()
  const ingredients = useIngredients()
  const name = (kind: string, id: string) =>
    (kind === 'category'
      ? categories.find((c) => c.id === id)?.name
      : ingredients.find((i) => i.id === id)?.name) ?? id.slice(0, 8)
  return (
    <AdminPage
      title="Food rules"
      intro="Shelf-life rules behind expiry estimates. Households change their own in Settings; this view is read-only."
    >
      {error && <ErrorNote error={error} />}
      <h2 className="t-label m-0 text-ink-muted">
        Household overrides ({data?.overrides.length ?? 0})
      </h2>
      <Table aria-label="Household overrides">
        <thead>
          <tr>
            <th>Household</th>
            <th>Applies to</th>
            <th>Shelf life</th>
          </tr>
        </thead>
        <tbody>
          {data?.overrides.map((r) => (
            <tr key={`${r.householdId}:${r.subjectId}`}>
              <td>{r.householdName}</td>
              <td>
                {name(r.subjectKind, r.subjectId)}{' '}
                <span className="text-ink-muted">({r.subjectKind})</span>
              </td>
              <td>{summary(r.shelfLife)}</td>
            </tr>
          ))}
          {data?.overrides.length === 0 && (
            <tr>
              <td colSpan={3} className="text-ink-muted">
                No household has changed a rule.
              </td>
            </tr>
          )}
        </tbody>
      </Table>
      <h2 className="t-label m-0 text-ink-muted">
        Stockpot defaults ({data?.defaults.length ?? 0})
      </h2>
      <Table aria-label="Defaults">
        <thead>
          <tr>
            <th>Category</th>
            <th>Shelf life</th>
          </tr>
        </thead>
        <tbody>
          {data?.defaults.map((r) => (
            <tr key={r.subjectId}>
              <td>{name(r.subjectKind, r.subjectId)}</td>
              <td>{summary(r.shelfLife)}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </AdminPage>
  )
}
