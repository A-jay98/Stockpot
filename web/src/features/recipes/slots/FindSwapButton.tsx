import { Replace, Search } from 'lucide-react'
import { useState } from 'react'
import { Button } from '../../../ui'
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
  if (line.status === 'sub' && line.sub?.note)
    return (
      <span className="inline-flex items-start gap-1.5 text-[13px] text-ink-muted">
        <Replace aria-hidden className="mt-px size-4 shrink-0 text-brand" />
        {line.sub.note}
      </span>
    )
  if (line.status !== 'missing' || !line.line.ingredient_id) return null
  return (
    <Button
      size="sm"
      variant="link"
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
