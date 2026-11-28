import { useLiveQuery } from 'dexie-react-hooks'
import { v7 } from 'uuid'
import { db, type StockpotDB } from '../../offline/db'
import { dispatch } from '../../offline/outbox'
import { registerReducer } from '../../offline/reducers'
import type { RecipeImport } from '../../offline/replica'

// Recipe import (architecture-m2 §7): each link, photo set or pasted text is a recipe_import row
// the server works through; its status syncs back, so the list below the form is live.

const MAX_BATCH = 50

const INSTAGRAM = /^(?:www\.|m\.)?(?:instagram\.com|instagr\.am)$/i

export const isInstagram = (u: string) => {
  try {
    return INSTAGRAM.test(new URL(u).hostname)
  } catch {
    return false
  }
}

/** The http(s) links in pasted text: one per line, or found inside shared text. */
export function parseLinks(text: string): { links: string[]; skipped: number } {
  const links: string[] = []
  let skipped = 0
  for (const line of text.split(/\r?\n/)) {
    const t = line.trim()
    if (!t) continue
    const found = t.match(/https?:\/\/[^\s<>"']+/gi)
    if (!found) {
      skipped++
      continue
    }
    for (const raw of found) {
      try {
        const u = new URL(raw.replace(/[),.;!?]+$/, ''))
        u.hash = ''
        if (!links.includes(u.toString())) links.push(u.toString())
      } catch {
        skipped++
      }
    }
  }
  return { links: links.slice(0, MAX_BATCH), skipped }
}

async function householdId(database: StockpotDB) {
  const h = (await database.meta.get('sync.household'))?.value
  return typeof h === 'string' ? h : null
}

registerReducer(
  'recipe.import.create',
  async (database, p, { clientTime }) => {
    if (await database.recipe_import.get(p.id)) return
    const kind = p.kind === 'url' && p.input && isInstagram(p.input) ? 'instagram' : p.kind
    await database.recipe_import.put({
      id: p.id,
      householdId: await householdId(database),
      createdAt: clientTime,
      updatedAt: clientTime,
      rev: 0,
      batchId: p.batchId ?? null,
      kind,
      input: p.input?.trim() || null,
      blobRefs: p.blobRefs ?? [],
      status: 'queued',
      error: null,
      recipeId: null,
      attempts: 0,
      detectedLang: null,
    } as RecipeImport)
  },
  { targets: (p) => [p.id], blobs: (p) => p.blobRefs ?? [] },
)

async function requeue(database: StockpotDB, id: string, clientTime: string) {
  const row = await database.recipe_import.get(id)
  if (!row || row.status === 'done') return
  await database.recipe_import.put({ ...row, status: 'queued', error: null, updatedAt: clientTime })
}

registerReducer('recipe.import.retry', async (database, p, { clientTime }) => {
  const row = await database.recipe_import.get(p.id)
  if (
    row &&
    (row.status === 'failed' || row.status === 'needs_input' || row.status === 'waiting_quota')
  )
    await requeue(database, p.id, clientTime)
})

registerReducer('recipe.import.provide', async (database, p, { clientTime }) => {
  await requeue(database, p.id, clientTime)
})

registerReducer('recipe.import.cancel', async (database, p) => {
  const row = await database.recipe_import.get(p.id)
  if (row && row.status !== 'done') await database.recipe_import.delete(p.id)
})

/** Queue one import per link (a bulk import shares a batch id). Returns the import ids. */
export async function importLinks(links: string[]): Promise<string[]> {
  const batchId = links.length > 1 ? v7() : null
  const ids: string[] = []
  for (const input of links.slice(0, MAX_BATCH)) {
    const id = v7()
    ids.push(id)
    await dispatch('recipe.import.create', {
      id,
      kind: isInstagram(input) ? 'instagram' : 'url',
      input,
      batchId,
    })
  }
  return ids
}

export async function importText(text: string): Promise<string> {
  const id = v7()
  await dispatch('recipe.import.create', { id, kind: 'text', input: text.trim() })
  return id
}

export async function importPhotos(blobRefs: string[], note?: string): Promise<string> {
  const id = v7()
  await dispatch('recipe.import.create', {
    id,
    kind: 'photo',
    blobRefs: blobRefs.slice(0, 4),
    input: note?.trim() || null,
  })
  return id
}

export const retryImport = (id: string) => dispatch('recipe.import.retry', { id })
export const cancelImport = (id: string) => dispatch('recipe.import.cancel', { id })
export const provideImport = (id: string, text: string) =>
  dispatch('recipe.import.provide', { id, text: text.trim() })

/** Imports, newest first (ids are uuidv7, so they sort by creation). */
export const useImports = (limit = 30): RecipeImport[] =>
  useLiveQuery(
    async () =>
      (await db.recipe_import.toArray()).sort((a, b) => b.id.localeCompare(a.id)).slice(0, limit),
    [limit],
    [] as RecipeImport[],
  )

export const inProgress = (s: RecipeImport['status']) =>
  s === 'queued' || s === 'fetching' || s === 'parsing' || s === 'translating'

/** What the row says while it works. */
export function statusText(row: RecipeImport): string {
  switch (row.status) {
    case 'queued':
      return 'Waiting to start…'
    case 'fetching':
      return row.kind === 'instagram' ? 'Reading the reel…' : 'Reading the page…'
    case 'parsing':
      return row.kind === 'photo' ? 'Reading the photos…' : 'Reading the recipe…'
    case 'translating':
      return 'Translating to English…'
    case 'waiting_quota':
      return 'Waiting for AI quota. It continues by itself.'
    case 'needs_input':
      return row.error?.detail ?? 'Paste the recipe text to continue.'
    case 'failed':
      return row.error?.detail ?? "Couldn't import this recipe."
    case 'done':
      return 'Imported'
  }
}

/** A short label for an import before it has a recipe: the site, "Instagram reel", … */
export function importLabel(row: RecipeImport): string {
  switch (row.kind) {
    case 'instagram':
      return 'Instagram reel'
    case 'photo':
      return row.blobRefs.length > 1
        ? `${row.blobRefs.length} photos of a recipe`
        : 'Photo of a recipe'
    case 'text': {
      const first =
        row.input
          ?.split('\n')
          .find((l) => l.trim())
          ?.trim() ?? 'Pasted text'
      return first.length > 60 ? `${first.slice(0, 59)}…` : first
    }
    default:
      try {
        const u = new URL(row.input ?? '')
        const slug = u.pathname.split('/').filter(Boolean).pop()?.replace(/[-_]+/g, ' ')
        return slug ? `${u.hostname.replace(/^www\./, '')} · ${slug}` : u.hostname
      } catch {
        return row.input ?? 'Link'
      }
  }
}
