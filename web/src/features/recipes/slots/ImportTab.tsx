import { useLiveQuery } from 'dexie-react-hooks'
import {
  AlertCircle,
  Camera,
  ChevronRight,
  Clapperboard,
  ClipboardPaste,
  Globe,
  Hourglass,
  Loader2,
  RotateCw,
  X,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { db } from '../../../offline/db'
import type { RecipeImport } from '../../../offline/replica'
import {
  Badge,
  Button,
  Field,
  List,
  ListItem,
  Segmented,
  Sheet,
  SheetContent,
  Textarea,
  toast,
} from '../../../ui'
import { BlobImage } from '../../photos/BlobImage'
import { PhotoPicker } from '../../photos/PhotoPicker'
import {
  cancelImport,
  importLabel,
  importLinks,
  importPhotos,
  importText,
  inProgress,
  parseLinks,
  provideImport,
  retryImport,
  statusText,
  useImports,
} from '../imports'
import { englishName, nativeName } from '../lang'
import { RecipeCover } from '../look'

type Mode = 'links' | 'photo' | 'text'

const modes: { value: Mode; label: string }[] = [
  { value: 'links', label: 'Links' },
  { value: 'photo', label: 'Photos' },
  { value: 'text', label: 'Text' },
]

/** What a shared link or text (PWA share target) brings in: ?url=…&text=…&title=… */
export function sharedInput(search: string): { mode: Mode; value: string } | null {
  const q = new URLSearchParams(search)
  const joined = [q.get('url'), q.get('text')].filter(Boolean).join('\n')
  if (!joined.trim()) return null
  if (parseLinks(joined).links.length > 0) return { mode: 'links', value: joined }
  const title = q.get('title')
  return { mode: 'text', value: title ? `${title}\n${joined}` : joined }
}

/** Import from links, Instagram, photos and text, with live progress. */
export function ImportTab() {
  const { search } = useLocation()
  const shared = useMemo(() => sharedInput(search), [search])
  const [mode, setMode] = useState<Mode>(shared?.mode ?? 'links')
  const imports = useImports()

  return (
    <div className="flex flex-col gap-4">
      <section className="flex flex-col gap-3 rounded-lg bg-surface p-4 shadow-card">
        <div className="flex flex-wrap gap-1.5" aria-hidden>
          <Badge tone="neutral">
            <Globe /> Recipe sites
          </Badge>
          <Badge tone="neutral">
            <Clapperboard /> Instagram reels
          </Badge>
          <Badge tone="neutral">
            <Camera /> Photo of a recipe
          </Badge>
          <Badge tone="neutral">
            <ClipboardPaste /> Pasted text
          </Badge>
        </div>
        <Segmented value={mode} onValueChange={setMode} options={modes} aria-label="Import from" />
        {mode === 'links' && <LinksForm initial={shared?.mode === 'links' ? shared.value : ''} />}
        {mode === 'photo' && <PhotosForm />}
        {mode === 'text' && <TextForm initial={shared?.mode === 'text' ? shared.value : ''} />}
        <p className="t-body-sm m-0 text-ink-muted">
          Recipe data on the page is read first. Reels use the caption and the spoken transcript.
          Anything not in English is translated, and the original is kept.
        </p>
      </section>
      {imports.length > 0 && (
        <section className="flex flex-col gap-2">
          <h2 className="t-title m-0 px-1">Imported</h2>
          <List aria-label="Imports">
            {imports.map((row) => (
              <ImportRow key={row.id} row={row} />
            ))}
          </List>
        </section>
      )}
    </div>
  )
}

function LinksForm({ initial }: { initial: string }) {
  const [text, setText] = useState(initial)
  useEffect(() => setText(initial), [initial])
  const { links, skipped } = parseLinks(text)
  const [busy, setBusy] = useState(false)

  async function submit() {
    setBusy(true)
    try {
      await importLinks(links)
      setText('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Field
        label="Paste links, one per line"
        hint={
          skipped > 0
            ? `${skipped} line${skipped === 1 ? '' : 's'} without a link skipped`
            : undefined
        }
      >
        {(p) => (
          <Textarea
            {...p}
            rows={4}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="https://www.bbcgoodfood.com/recipes/…&#10;https://www.instagram.com/reel/…"
            inputMode="url"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
          />
        )}
      </Field>
      <Button block disabled={busy || links.length === 0} onClick={submit}>
        {links.length > 1 ? `Import ${links.length} recipes` : 'Import recipe'}
      </Button>
    </>
  )
}

function PhotosForm() {
  const [refs, setRefs] = useState<string[]>([])
  const [note, setNote] = useState('')

  async function submit() {
    await importPhotos(refs, note)
    setRefs([])
    setNote('')
  }

  return (
    <>
      <p className="t-body-sm m-0 text-ink-muted">
        A cookbook page, a handwritten card or a screenshot. Up to 4 photos of the same recipe, in
        order.
      </p>
      {refs.length > 0 && (
        <ul className="m-0 flex list-none gap-2 p-0" aria-label="Chosen photos">
          {refs.map((r, i) => (
            <li key={r} className="relative">
              <BlobImage
                blobRef={r}
                w={256}
                alt={`Photo ${i + 1}`}
                className="size-16 rounded-md object-cover"
              />
              <button
                type="button"
                aria-label={`Remove photo ${i + 1}`}
                className="absolute -top-1.5 -right-1.5 grid size-6 place-items-center rounded-full bg-ink text-bg"
                onClick={() => setRefs(refs.filter((x) => x !== r))}
              >
                <X className="size-3.5" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )}
      {refs.length < 4 && (
        <PhotoPicker
          className="flex-wrap"
          onPicked={(picked) => setRefs([...refs, ...picked].slice(0, 4))}
        />
      )}
      {refs.length > 0 && (
        <Field label="Note (optional)">
          {(p) => (
            <Textarea
              {...p}
              rows={2}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="Anything the photos don't show"
            />
          )}
        </Field>
      )}
      <Button block disabled={refs.length === 0} onClick={submit}>
        Import from {refs.length > 1 ? `${refs.length} photos` : 'photo'}
      </Button>
    </>
  )
}

function TextForm({ initial }: { initial: string }) {
  const [text, setText] = useState(initial)
  useEffect(() => setText(initial), [initial])

  async function submit() {
    await importText(text)
    setText('')
  }

  return (
    <>
      <Field label="Paste the recipe">
        {(p) => (
          <Textarea
            {...p}
            rows={8}
            maxLength={20000}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Title, ingredients and steps, in any language"
          />
        )}
      </Field>
      <Button block disabled={text.trim().length < 20} onClick={submit}>
        Import recipe
      </Button>
    </>
  )
}

function ImportRow({ row }: { row: RecipeImport }) {
  const recipe = useLiveQuery(
    async () => (row.recipeId ? ((await db.recipe.get(row.recipeId)) ?? null) : null),
    [row.recipeId],
  )
  const [providing, setProviding] = useState(false)

  if (row.status === 'done' && recipe) {
    const lang = recipe.sourceLang && recipe.sourceLang !== 'en' ? recipe.sourceLang : null
    const from =
      row.kind === 'instagram'
        ? 'From an Instagram reel'
        : row.kind === 'photo'
          ? 'From photos'
          : row.kind === 'text'
            ? 'From pasted text'
            : 'From a recipe site'
    return (
      <ListItem
        asChild
        leading={
          <RecipeCover
            recipeId={recipe.id}
            coverBlob={recipe.coverBlob}
            w={256}
            alt=""
            icon={null}
            size={19}
            className="size-full rounded-[10px]"
          />
        }
        title={recipe.title}
        subtitle={
          <span className="flex flex-col">
            <span>{lang ? `${from} · translated from ${englishName(lang)}` : from}</span>
            {lang && (
              <span>
                <Badge tone="brand">
                  English · <span lang={lang}>{nativeName(lang)}</span>
                </Badge>
              </span>
            )}
          </span>
        }
        trailing={<ChevronRight className="size-5 text-ink-muted" aria-hidden />}
      >
        <Link to={`/recipes/${recipe.id}`} />
      </ListItem>
    )
  }

  const working = inProgress(row.status) || (row.status === 'done' && !recipe)
  const icon = working ? (
    <Loader2 className="animate-spin" aria-hidden />
  ) : row.status === 'waiting_quota' ? (
    <Hourglass aria-hidden />
  ) : (
    <AlertCircle className="text-expired" aria-hidden />
  )
  return (
    <ListItem
      leading={icon}
      title={importLabel(row)}
      subtitle={
        <span aria-live="polite">
          {working && row.status === 'done' ? 'Syncing…' : statusText(row)}
        </span>
      }
      trailing={
        <span className="flex items-center gap-1">
          {row.status === 'needs_input' && (
            <Sheet open={providing} onOpenChange={setProviding}>
              <Button size="sm" onClick={() => setProviding(true)}>
                Paste text
              </Button>
              <ProvideSheet row={row} onDone={() => setProviding(false)} />
            </Sheet>
          )}
          {row.status === 'failed' && (
            <Button size="sm" variant="secondary" onClick={() => retryImport(row.id)}>
              <RotateCw aria-hidden />
              Retry
            </Button>
          )}
          {row.status !== 'done' && (
            <Button
              size="icon"
              variant="ghost"
              aria-label={`Cancel import of ${importLabel(row)}`}
              onClick={() => cancelImport(row.id)}
            >
              <X aria-hidden />
            </Button>
          )}
        </span>
      }
    />
  )
}

function ProvideSheet({ row, onDone }: { row: RecipeImport; onDone: () => void }) {
  const [text, setText] = useState('')
  return (
    <SheetContent
      title="Paste the recipe"
      description={
        row.kind === 'instagram'
          ? "Instagram didn't let us read this reel. Copy the caption (or type the recipe) and paste it here."
          : statusText(row)
      }
      footer={
        <Button
          block
          disabled={text.trim().length < 20}
          onClick={async () => {
            await provideImport(row.id, text)
            toast({ tone: 'success', title: 'Thanks, importing it now' })
            onDone()
          }}
        >
          Continue import
        </Button>
      }
    >
      <Field label="Recipe text">
        {(p) => (
          <Textarea
            {...p}
            rows={8}
            maxLength={20000}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        )}
      </Field>
    </SheetContent>
  )
}
