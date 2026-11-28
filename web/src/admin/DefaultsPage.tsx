import { useState } from 'react'
import { ApiError } from '../api/problem'
import { Badge, Input, toast } from '../ui'
import { unitCodes } from './ReportsPage'
import { useDefaults, useSetDefaultUnit } from './reports-api'
import { AdminPage, compactField, ErrorNote, formatTime, Table } from './ui'

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

/** Ingredient default (purchase) units, editable, with the change history. */
export function DefaultsPage() {
  const [q, setQ] = useState('')
  const { data, error } = useDefaults(q.trim())
  const set = useSetDefaultUnit()

  return (
    <AdminPage
      title="Defaults"
      intro="The unit each ingredient is usually bought in. An empty unit inherits the parent's. Changes from reports and from here are audited and listed below; global ingredients change for every household."
    >
      {error && <ErrorNote error={error} />}
      <Input
        aria-label="Search ingredients"
        placeholder="Search ingredients"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        className="max-w-sm"
      />
      <Table aria-label="Ingredient defaults">
        <thead>
          <tr>
            <th>Ingredient</th>
            <th>Scope</th>
            <th>Kind of</th>
            <th>Unit</th>
            <th>Products</th>
            <th>Open reports</th>
          </tr>
        </thead>
        <tbody>
          {data?.ingredients.length === 0 && (
            <tr>
              <td colSpan={6} className="text-ink-muted">
                No ingredients match.
              </td>
            </tr>
          )}
          {data?.ingredients.map((i) => (
            <tr key={i.id}>
              <td>
                {i.name} <span className="font-mono text-[12px] text-ink-muted">{i.slug}</span>
              </td>
              <td>{i.household ?? 'Global'}</td>
              <td>{i.parent ?? '—'}</td>
              <td>
                <select
                  aria-label={`Unit for ${i.name}`}
                  className={compactField}
                  value={i.unit ?? ''}
                  disabled={set.isPending}
                  onChange={(e) =>
                    set.mutate(
                      { id: i.id, unit: e.target.value || null },
                      {
                        onSuccess: () =>
                          toast({
                            title: `${i.name}: ${e.target.value || 'inherit'}`,
                            tone: 'success',
                          }),
                        onError: (err) =>
                          toast({
                            title: 'Could not save',
                            description: errorText(err),
                            tone: 'error',
                          }),
                      },
                    )
                  }
                >
                  <option value="">{i.inheritedUnit ? `inherit (${i.inheritedUnit})` : '—'}</option>
                  {unitCodes.map((u) => (
                    <option key={u} value={u}>
                      {u}
                    </option>
                  ))}
                </select>
              </td>
              <td className="tabular">{i.products}</td>
              <td>{i.openReports > 0 ? <Badge tone="soon">{i.openReports}</Badge> : '—'}</td>
            </tr>
          ))}
        </tbody>
      </Table>
      <section aria-label="History" className="flex flex-col gap-2">
        <h2 className="t-label m-0 text-ink-muted">History</h2>
        <Table aria-label="Default changes">
          <thead>
            <tr>
              <th>When</th>
              <th>What</th>
              <th>Change</th>
              <th>By</th>
            </tr>
          </thead>
          <tbody>
            {data?.history.length === 0 && (
              <tr>
                <td colSpan={4} className="text-ink-muted">
                  No changes yet.
                </td>
              </tr>
            )}
            {data?.history.map((h) => (
              <tr key={`${h.time}-${h.targetId}`}>
                <td className="whitespace-nowrap">{formatTime(h.time)}</td>
                <td>
                  {h.name} <span className="text-ink-muted">({h.targetType})</span>
                </td>
                <td>
                  {h.before ?? '—'} → {h.after ?? '—'}
                </td>
                <td>
                  {h.actor}
                  {h.reportId && <span className="text-ink-muted"> · from a report</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </section>
    </AdminPage>
  )
}
