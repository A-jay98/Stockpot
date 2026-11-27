import { Pencil, Plus, Trash2 } from 'lucide-react'
import { type FormEvent, type ReactNode, useState } from 'react'
import { Button, Input, List, ListItem, Sheet, SheetContent } from '../../ui'

export type ManagedRow = { id: string; name: string; subtitle?: string }

type Props = {
  /** Noun for labels: "store", "location". */
  noun: string
  rows: ManagedRow[]
  onAdd: (name: string) => void
  onRename: (id: string, name: string) => void
  onDelete: (id: string) => void
  /** Names offered as one-tap adds (skipped if already present). */
  quickPicks?: string[]
  /** Extra controls next to the add field (e.g. a kind picker). */
  addExtra?: ReactNode
}

/** Simple add / rename / delete list used by the Stores and Locations settings screens. */
export function ManageList({
  noun,
  rows,
  onAdd,
  onRename,
  onDelete,
  quickPicks = [],
  addExtra,
}: Props) {
  const [name, setName] = useState('')
  const [editing, setEditing] = useState<ManagedRow | null>(null)
  const [editName, setEditName] = useState('')
  const [deleting, setDeleting] = useState<ManagedRow | null>(null)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const v = name.trim()
    if (!v) return
    onAdd(v)
    setName('')
  }
  const have = new Set(rows.map((r) => r.name.toLowerCase()))
  const picks = quickPicks.filter((q) => !have.has(q.toLowerCase()))

  return (
    <>
      <form onSubmit={submit} className="flex flex-col gap-2">
        {addExtra}
        <div className="flex gap-2">
          <Input
            aria-label={`New ${noun} name`}
            placeholder={`Add a ${noun}`}
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={100}
          />
          <Button type="submit" disabled={!name.trim()}>
            <Plus aria-hidden />
            Add
          </Button>
        </div>
      </form>
      {picks.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {picks.map((q) => (
            <Button key={q} size="sm" variant="secondary" onClick={() => onAdd(q)}>
              <Plus aria-hidden />
              {q}
            </Button>
          ))}
        </div>
      )}
      {rows.length === 0 ? (
        <p className="t-body-sm m-0 text-ink-muted">No {noun}s yet.</p>
      ) : (
        <List>
          {rows.map((r) => (
            <ListItem
              key={r.id}
              title={r.name}
              subtitle={r.subtitle}
              trailing={
                <span className="flex gap-1">
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label={`Rename ${r.name}`}
                    onClick={() => {
                      setEditing(r)
                      setEditName(r.name)
                    }}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label={`Delete ${r.name}`}
                    onClick={() => setDeleting(r)}
                  >
                    <Trash2 />
                  </Button>
                </span>
              }
            />
          ))}
        </List>
      )}

      <Sheet open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <SheetContent
          title={`Rename ${noun}`}
          footer={
            <Button
              block
              disabled={!editName.trim()}
              onClick={() => {
                if (editing) onRename(editing.id, editName.trim())
                setEditing(null)
              }}
            >
              Save
            </Button>
          }
        >
          <Input
            aria-label="Name"
            value={editName}
            onChange={(e) => setEditName(e.target.value)}
            maxLength={100}
          />
        </SheetContent>
      </Sheet>

      <Sheet open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <SheetContent
          title={`Delete ${deleting?.name ?? noun}?`}
          description="Items that use it keep their data but lose this link."
          footer={
            <Button
              block
              variant="danger"
              onClick={() => {
                if (deleting) onDelete(deleting.id)
                setDeleting(null)
              }}
            >
              Delete
            </Button>
          }
        >
          <span />
        </SheetContent>
      </Sheet>
    </>
  )
}
