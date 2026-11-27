import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap } from '../../api/client'
import { Page } from '../../app/Page'
import { Button, Field, Input, List, ListItem, Sheet, SheetContent, toast } from '../../ui'
import { useCategories, useIngredients } from '../catalog/hooks'
import { type FoodRule, foodRulesKey, useFoodRules } from '../inventory/foodRules'

const keys = [
  ['fridge', 'Fridge'],
  ['freezer', 'Freezer'],
  ['pantry', 'Pantry'],
  ['opened_fridge', 'Opened, in the fridge'],
  ['thawed_fridge', 'Thawed, in the fridge'],
] as const

type Subject = { kind: 'category' | 'ingredient'; id: string; name: string }
type Shelf = FoodRule['shelfLife']

export const summary = (s: Shelf) =>
  keys
    .filter(([k]) => s[k] !== undefined)
    .map(([k, label]) => `${label.split(',')[0]?.toLowerCase()} ${s[k]}d`)
    .join(' · ')

/** Shelf-life rules: Stockpot's defaults per category, plus the household's overrides. */
export function FoodRulesPage() {
  const { data, error } = useFoodRules()
  const categories = useCategories()
  const ingredients = useIngredients()
  const [editing, setEditing] = useState<Subject | null>(null)
  const [q, setQ] = useState('')

  const rules = data ?? []
  const find = (kind: Subject['kind'], id: string, def: boolean) =>
    rules.find(
      (r) => r.subjectKind === kind && r.subjectId === id && (r.householdId === undefined) === def,
    )
  const overrides = rules.filter(
    (r) => r.householdId !== undefined && r.subjectKind === 'ingredient',
  )
  const pickable = q.trim()
    ? ingredients.filter((i) => i.name.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 6)
    : []

  return (
    <Page title="Food rules">
      <p className="t-body-sm m-0 text-ink-muted">
        How long things keep. Stockpot estimates an expiry from these when you add, open, freeze or
        thaw something without a date. They are guidance, not a guarantee: check before eating.
      </p>
      {error && (
        <p className="t-body-sm m-0 text-expired">Couldn’t load rules. Check your connection.</p>
      )}

      {overrides.length > 0 && (
        <section aria-label="Your ingredient rules" className="flex flex-col gap-2">
          <h2 className="t-label m-0 text-ink-muted">Your ingredient rules</h2>
          <List>
            {overrides.map((r) => {
              const name = ingredients.find((i) => i.id === r.subjectId)?.name ?? 'Ingredient'
              return (
                <ListItem
                  key={r.subjectId}
                  title={name}
                  subtitle={summary(r.shelfLife)}
                  trailing={
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => setEditing({ kind: 'ingredient', id: r.subjectId, name })}
                    >
                      Edit
                    </Button>
                  }
                />
              )
            })}
          </List>
        </section>
      )}

      <section aria-label="Categories" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">By category</h2>
        <List>
          {categories
            .filter((c) => find('category', c.id, true) || find('category', c.id, false))
            .map((c) => {
              const own = find('category', c.id, false)
              const def = find('category', c.id, true)
              return (
                <ListItem
                  key={c.id}
                  title={c.name}
                  subtitle={summary((own ?? def)?.shelfLife ?? {})}
                  trailing={
                    <>
                      {own && <span className="text-[12.5px] font-semibold text-brand">Yours</span>}
                      <Button
                        size="sm"
                        variant="secondary"
                        aria-label={`Edit ${c.name}`}
                        onClick={() => setEditing({ kind: 'category', id: c.id, name: c.name })}
                      >
                        Edit
                      </Button>
                    </>
                  }
                />
              )
            })}
        </List>
      </section>

      <section aria-label="Add an ingredient rule" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">Rule for one ingredient</h2>
        <Input
          aria-label="Find an ingredient"
          placeholder="Find an ingredient"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {pickable.map((i) => (
          <button
            key={i.id}
            type="button"
            className="rounded-md border-0 bg-surface px-3 py-2.5 text-left font-semibold text-ink shadow-card"
            onClick={() => {
              setEditing({ kind: 'ingredient', id: i.id, name: i.name })
              setQ('')
            }}
          >
            {i.name}
          </button>
        ))}
      </section>

      {editing && (
        <RuleSheet
          key={`${editing.kind}:${editing.id}`}
          subject={editing}
          own={find(editing.kind, editing.id, false)}
          def={find(editing.kind, editing.id, true)}
          onClose={() => setEditing(null)}
        />
      )}
    </Page>
  )
}

function RuleSheet({
  subject,
  own,
  def,
  onClose,
}: {
  subject: Subject
  own?: FoodRule
  def?: FoodRule
  onClose: () => void
}) {
  const qc = useQueryClient()
  const start = own?.shelfLife ?? def?.shelfLife ?? {}
  const [vals, setVals] = useState<Record<string, string>>(
    Object.fromEntries(keys.map(([k]) => [k, start[k] !== undefined ? String(start[k]) : ''])),
  )
  const done = () => {
    void qc.invalidateQueries({ queryKey: foodRulesKey })
    onClose()
  }
  const save = useMutation({
    mutationFn: () => {
      const shelfLife = Object.fromEntries(
        keys.filter(([k]) => Number(vals[k]) > 0).map(([k]) => [k, Math.floor(Number(vals[k]))]),
      ) as Shelf
      return unwrap(
        api.PUT('/v1/food-rules/{subjectKind}/{subjectId}', {
          params: { path: { subjectKind: subject.kind, subjectId: subject.id } },
          body: { shelfLife },
        }),
      )
    },
    onSuccess: done,
    onError: () => toast({ title: 'Couldn’t save the rule', tone: 'error' }),
  })
  const reset = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/v1/food-rules/{subjectKind}/{subjectId}', {
          params: { path: { subjectKind: subject.kind, subjectId: subject.id } },
        }),
      ),
    onSuccess: done,
    onError: () => toast({ title: 'Couldn’t reset the rule', tone: 'error' }),
  })
  const any = keys.some(([k]) => Number(vals[k]) > 0)
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent
        title={subject.name}
        description="Days it keeps. Leave a box empty if you don’t know."
        footer={
          <>
            {own && (
              <Button variant="secondary" onClick={() => reset.mutate()} disabled={reset.isPending}>
                Reset to default
              </Button>
            )}
            <Button
              className="flex-1"
              onClick={() => save.mutate()}
              disabled={!any || save.isPending}
            >
              Save
            </Button>
          </>
        }
      >
        {keys.map(([k, label]) => (
          <Field key={k} label={`${label} (days)`}>
            {(props) => (
              <Input
                {...props}
                inputMode="numeric"
                value={vals[k]}
                onChange={(e) => setVals({ ...vals, [k]: e.target.value })}
              />
            )}
          </Field>
        ))}
      </SheetContent>
    </Sheet>
  )
}
