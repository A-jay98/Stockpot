import { Replace, Search } from 'lucide-react'
import { useState } from 'react'
import { Badge, Button } from '../../../ui'
import { requestSwap } from '../match/assist'
import { useRecipeMatch } from '../match/useKitchen'

/**
 * "Find a swap" for one ingredient line: shows the swap that covers it when there is one, a button
 * that asks the assistant when the line is missing (and matched), and nothing otherwise.
 */
export function FindSwapButton({
  recipeId,
  versionId,
  lineId,
}: {
  recipeId: string
  versionId: string
  lineId: string
}) {
  const match = useRecipeMatch(recipeId, versionId)
  const [busy, setBusy] = useState(false)
  const line = match?.ranked.lines.find((l) => l.line.line_id === lineId)
  if (!line) return null
  if (line.status === 'sub' && line.sub) {
    // Compact: it sits at the end of an ingredient row. The full note is the tooltip/label.
    const names = line.sub.uses
      .map((u) => match?.kitchen.ingredients.get(u.ingredient_id)?.name)
      .filter(Boolean)
      .join(' + ')
    const note = line.sub.note ?? `Use ${names}`
    return (
      <Badge
        tone="brand"
        title={note}
        aria-label={`Swap: ${note}`}
        className="max-w-[45%] shrink-0 overflow-hidden"
      >
        <Replace aria-hidden />
        <span className="truncate">{names || 'Swap'}</span>
      </Badge>
    )
  }
  if (line.status !== 'missing' || !line.line.ingredient_id) return null
  return (
    <Button
      size="sm"
      variant="link"
      className="shrink-0"
      disabled={busy}
      onClick={async () => {
        setBusy(true)
        await requestSwap(recipeId, versionId, lineId)
        setBusy(false)
      }}
    >
      <Search aria-hidden />
      {busy ? 'Looking…' : 'Find a swap'}
    </Button>
  )
}
