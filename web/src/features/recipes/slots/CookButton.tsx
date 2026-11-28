import { CookingPot } from 'lucide-react'
import { Link } from 'react-router'
import { Button } from '../../../ui'

/** "I cooked this": opens the cook sheet. */
export function CookButton({ recipeId }: { recipeId: string; versionId: string }) {
  return (
    <Button asChild>
      <Link to={`/recipes/${recipeId}/cook`}>
        <CookingPot aria-hidden />I cooked this
      </Link>
    </Button>
  )
}
