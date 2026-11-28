import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap } from '../../api/client'
import { ApiError } from '../../api/problem'
import type { components } from '../../api/schema'
import { Button, cn, Field, Sheet, SheetContent, Textarea, toast } from '../../ui'

export type ReportKind = components['schemas']['ReportKind']
export type ReportSource = components['schemas']['ReportSource']

/** Units a person can say an item is bought in. */
const purchaseUnits = [
  'ea',
  'pack',
  'bag',
  'bottle',
  'can',
  'jar',
  'box',
  'bunch',
  'head',
  'g',
  'kg',
  'lb',
  'oz',
  'ml',
  'l',
]

const kinds: { value: ReportKind; label: string; hint: string }[] = [
  { value: 'wrong_unit', label: 'The default unit is wrong', hint: 'e.g. salt should be a bag' },
  { value: 'wrong_match', label: 'It is matched to the wrong food', hint: '' },
  { value: 'wrong_icon', label: 'The icon is wrong', hint: '' },
  { value: 'other', label: 'Something else', hint: '' },
]

type Props = {
  open: boolean
  onClose: () => void
  source: ReportSource
  subjectId: string
  /** What the item is called, for the title. */
  title: string
  /** The unit the item shows now. */
  unit?: string | null
}

/** "Report a problem" for one item. Reports are reviewed in the background. */
export function ReportSheet(props: Props) {
  return (
    <Sheet open={props.open} onOpenChange={(o) => !o && props.onClose()}>
      {props.open && <Body {...props} />}
    </Sheet>
  )
}

function Body({ onClose, source, subjectId, title, unit }: Props) {
  const [kind, setKind] = useState<ReportKind>('wrong_unit')
  const [suggested, setSuggested] = useState('')
  const [note, setNote] = useState('')
  const send = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/v1/reports', {
          body: {
            kind,
            source,
            subjectId,
            ...(kind === 'wrong_unit' && suggested ? { suggested } : {}),
            ...(note.trim() ? { note: note.trim() } : {}),
          },
        }),
      ),
    onSuccess: () => {
      toast({ title: 'Thanks, we will look into it', tone: 'success' })
      onClose()
    },
    onError: (e) =>
      toast({
        title: 'Could not send the report',
        description: !navigator.onLine
          ? 'You are offline. Try again later.'
          : e instanceof ApiError && e.status === 404
            ? 'This item has not synced yet. Try again in a moment.'
            : (e as Error).message,
        tone: 'error',
      }),
  })
  const valid = kind !== 'other' || note.trim() !== ''

  return (
    <SheetContent
      title="Report a problem"
      description={title}
      footer={
        <Button block onClick={() => send.mutate()} disabled={!valid || send.isPending}>
          Send report
        </Button>
      }
    >
      <fieldset className="m-0 flex flex-col gap-1.5 border-0 p-0">
        <legend className="t-label p-0 pb-1.5 text-ink-muted">What is wrong?</legend>
        {kinds.map((k) => (
          <button
            key={k.value}
            type="button"
            aria-pressed={kind === k.value}
            onClick={() => setKind(k.value)}
            className={cn(
              'flex flex-col items-start rounded-md border border-border bg-surface px-3 py-2.5 text-left',
              kind === k.value && 'border-brand bg-brand-soft',
            )}
          >
            <span className="font-semibold">{k.label}</span>
            {k.hint && <span className="t-body-sm text-ink-muted">{k.hint}</span>}
          </button>
        ))}
      </fieldset>
      {kind === 'wrong_unit' && (
        <Field
          label={unit ? `It shows "${unit}". It is usually bought as` : 'It is usually bought as'}
        >
          {(p) => (
            <select
              {...p}
              className="rounded-md border border-border bg-surface px-3 py-[11px] text-base"
              value={suggested}
              onChange={(e) => setSuggested(e.target.value)}
            >
              <option value="">Not sure</option>
              {purchaseUnits.map((u) => (
                <option key={u} value={u}>
                  {u}
                </option>
              ))}
            </select>
          )}
        </Field>
      )}
      <Field label={kind === 'other' ? 'What is wrong' : 'Note (optional)'}>
        {(p) => (
          <Textarea {...p} value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
        )}
      </Field>
    </SheetContent>
  )
}
