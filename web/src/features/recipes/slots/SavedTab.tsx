import { Clock, Users } from 'lucide-react'
import { Link } from 'react-router'
import { List, ListItem } from '../../../ui'
import { useSavedRecipes } from '../hooks'
import { ComingSoon } from './ComingSoon'

/** Saved recipes. A plain list for now; m2-3 replaces it with the library (search, tags, covers). */
export function SavedTab() {
  const recipes = useSavedRecipes()
  if (recipes.length === 0) return <ComingSoon>No saved recipes yet.</ComingSoon>
  return (
    <List>
      {recipes.map((r) => (
        <ListItem
          key={r.id}
          asChild
          title={r.title}
          subtitle={<RecipeMeta servings={r.servings} minutes={r.totalMinutes} />}
        >
          <Link to={`/recipes/${r.id}`} />
        </ListItem>
      ))}
    </List>
  )
}

export function RecipeMeta({
  servings,
  minutes,
}: {
  servings: number | null
  minutes: number | null
}) {
  if (!servings && !minutes) return null
  return (
    <span className="inline-flex items-center gap-3">
      {servings ? (
        <span className="inline-flex items-center gap-1">
          <Users className="size-3.5" aria-hidden /> {servings} servings
        </span>
      ) : null}
      {minutes ? (
        <span className="inline-flex items-center gap-1">
          <Clock className="size-3.5" aria-hidden /> {minutes} min
        </span>
      ) : null}
    </span>
  )
}
