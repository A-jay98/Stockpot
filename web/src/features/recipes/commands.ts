import { v7 } from 'uuid'
import type { CommandPayloads } from '../../offline/commands'
import type { StockpotDB } from '../../offline/db'
import { registerReducer } from '../../offline/reducers'
import type {
  Recipe,
  RecipeLine,
  RecipePhoto,
  RecipeStep,
  RecipeVersion,
} from '../../offline/replica'

// Optimistic recipe reducers. The server parses and matches ingredient lines (architecture-m2 §4);
// until the next pull a new version shows its raw lines. Each reducer sets fields, so replays after
// a pull are harmless.

type Content = CommandPayloads['recipe.version.create']

async function base(db: StockpotDB, id: string, clientTime: string) {
  const household = (await db.meta.get('sync.household'))?.value
  return {
    id,
    householdId: typeof household === 'string' ? household : null,
    createdAt: clientTime,
    updatedAt: clientTime,
    rev: 0,
  }
}

// Leading quantity as the server sees it after normalising: ASCII, Persian or Arabic digits, or a
// unicode fraction (see `qtyStart` in internal/recipes/lines).
const QTY_START = /^[\d۰-۹٠-٩¼½¾⅐⅑⅒⅓⅔⅕⅖⅗⅘⅙⅚⅛⅜⅝⅞]/

/** Section header ("For the rice:"), mirroring Go's `lines.Header` (≤ 80 bytes, no quantity). */
export function isHeader(raw: string): boolean {
  const s = raw.trim()
  if (!s.endsWith(':') || s.length < 2 || new TextEncoder().encode(s).length > 80) return false
  const name = s.slice(0, -1).trim()
  return name !== '' && !QTY_START.test(name)
}

/**
 * Raw lines as shown before the server parses them: headers ("For the rice:") group what follows.
 * These line ids are client-only (the server assigns its own), so `recipe.line.set_ingredient`
 * must only target versions that came back from a pull (`rev > 0`).
 */
export function rawLines(raws: string[]): RecipeLine[] {
  const out: RecipeLine[] = []
  let group: string | undefined
  for (const r of raws) {
    const raw = r.trim()
    if (!raw) continue
    if (isHeader(raw)) {
      group = raw.slice(0, -1).trim()
      continue
    }
    out.push({ line_id: v7(), group, raw, name: raw, optional: false, staple: false })
  }
  return out
}

const cleanSteps = (steps: RecipeStep[]) =>
  steps.map((s) => ({ ...s, text: s.text.trim() })).filter((s) => s.text !== '')

async function writeVersion(
  db: StockpotDB,
  recipe: Recipe,
  v: Omit<RecipeVersion, 'n' | 'createdAt' | 'updatedAt' | 'rev' | 'householdId'>,
  clientTime: string,
) {
  if (!(await db.recipe_version.get(v.id))) {
    const ns = (await db.recipe_version.where('recipeId').equals(recipe.id).toArray()).map(
      (x) => x.n,
    )
    await db.recipe_version.put({
      ...(await base(db, v.id, clientTime)),
      ...v,
      n: Math.max(0, ...ns) + 1,
    })
  }
  await db.recipe.put({
    ...recipe,
    currentVersionId: v.id,
    title: v.title,
    servings: v.servings,
    totalMinutes: v.totalMinutes,
  })
}

function versionFields(p: Content | CommandPayloads['recipe.create']) {
  return {
    title: p.title.trim(),
    servings: p.servings ?? null,
    totalMinutes: p.totalMinutes ?? null,
    ingredients: rawLines(p.ingredients),
    steps: cleanSteps(p.steps),
    notes: p.notes ?? null,
  }
}

registerReducer(
  'recipe.create',
  async (db, p, { clientTime }) => {
    if (await db.recipe.get(p.id)) return
    const recipe: Recipe = {
      ...(await base(db, p.id, clientTime)),
      title: p.title.trim(),
      subtitle: p.subtitle ?? null,
      sourceKind: p.sourceKind ?? 'manual',
      sourceUrl: p.sourceUrl ?? null,
      sourceLang: p.sourceLang ?? null,
      currentVersionId: null,
      servings: null,
      totalMinutes: null,
      coverBlob: null,
      tags: [...new Set((p.tags ?? []).map((t) => t.trim().toLowerCase()).filter(Boolean))],
      saved: true,
      ideaExpiresAt: null,
      cookedCount: 0,
      lastCookedAt: null,
      importId: null,
    }
    await writeVersion(
      db,
      recipe,
      {
        id: p.versionId,
        recipeId: p.id,
        parentVersionId: null,
        origin: 'manual',
        changeSummary: null,
        original: null,
        originalVersionId: null,
        createdBy: null,
        ...versionFields(p),
      },
      clientTime,
    )
  },
  { targets: (p) => [p.id, p.versionId] },
)

registerReducer(
  'recipe.version.create',
  async (db, p, { clientTime }) => {
    const recipe = await db.recipe.get(p.recipeId)
    if (!recipe) return
    await writeVersion(
      db,
      recipe,
      {
        id: p.versionId,
        recipeId: p.recipeId,
        parentVersionId: p.parentVersionId ?? recipe.currentVersionId,
        origin: p.origin,
        changeSummary: p.changeSummary ?? null,
        original: null,
        originalVersionId: null,
        createdBy: null,
        ...versionFields(p),
      },
      clientTime,
    )
  },
  { targets: (p) => [p.recipeId, p.versionId] },
)

registerReducer(
  'recipe.version.restore',
  async (db, p, { clientTime }) => {
    const [recipe, from] = await Promise.all([
      db.recipe.get(p.recipeId),
      db.recipe_version.get(p.fromVersionId),
    ])
    if (!recipe || !from || from.recipeId !== recipe.id) return
    await writeVersion(
      db,
      recipe,
      {
        id: p.versionId,
        recipeId: recipe.id,
        parentVersionId: recipe.currentVersionId,
        origin: 'restore',
        changeSummary: `Restored v${from.n}`,
        title: from.title,
        servings: from.servings,
        totalMinutes: from.totalMinutes,
        ingredients: from.ingredients,
        steps: from.steps,
        notes: from.notes,
        original: null,
        originalVersionId: from.original ? from.id : from.originalVersionId,
        createdBy: null,
      },
      clientTime,
    )
  },
  { targets: (p) => [p.recipeId, p.versionId] },
)

registerReducer(
  'recipe.update',
  async (db, p) => {
    const row = await db.recipe.get(p.id)
    if (!row) return
    const next: Recipe = { ...row }
    if (p.tags !== undefined)
      next.tags = [...new Set((p.tags ?? []).map((t) => t.trim().toLowerCase()).filter(Boolean))]
    if (p.saved != null) {
      next.saved = p.saved
      if (p.saved) next.ideaExpiresAt = null
    }
    if (p.coverBlob !== undefined) next.coverBlob = p.coverBlob
    if (p.subtitle !== undefined) next.subtitle = p.subtitle?.trim() || null
    await db.recipe.put(next)
  },
  // A new cover is a photo: the outbox uploads it before sending the command.
  { blobs: (p) => (p.coverBlob ? [p.coverBlob] : []) },
)

registerReducer('recipe.delete', async (db, p) => {
  await db.recipe_version.where('recipeId').equals(p.id).delete()
  await db.recipe_photo.where('recipeId').equals(p.id).delete()
  await db.recipe.delete(p.id)
})

registerReducer('recipe.line.set_ingredient', async (db, p) => {
  const v = await db.recipe_version.get(p.versionId)
  if (!v) return
  await db.recipe_version.put({
    ...v,
    ingredients: v.ingredients.map((l) => {
      if (l.line_id !== p.lineId) return l
      const { ingredient_id: _i, match_confidence: _m, ...rest } = l
      return p.ingredientId ? { ...rest, ingredient_id: p.ingredientId, match_confidence: 1 } : rest
    }),
  })
})

registerReducer(
  'recipe.photo.add',
  async (db, p, { clientTime }) => {
    if (await db.recipe_photo.get(p.id)) return
    if (!(await db.recipe.get(p.recipeId))) return
    const photo: RecipePhoto = {
      ...(await base(db, p.id, clientTime)),
      recipeId: p.recipeId,
      cookSessionId: p.cookSessionId ?? null,
      blobRef: p.blobRef,
      takenOn: p.takenOn ?? null,
      caption: p.caption?.trim() || null,
      createdBy: null,
    }
    await db.recipe_photo.put(photo)
  },
  { targets: (p) => [p.id], blobs: (p) => [p.blobRef] },
)

registerReducer('recipe.photo.remove', async (db, p) => {
  await db.recipe_photo.delete(p.id)
})

/** The inverse of rawLines: a version's lines as raw text, with a header before each new group. */
export function versionRawLines(lines: RecipeLine[]): string[] {
  const out: string[] = []
  let group: string | undefined
  for (const l of lines) {
    if ((l.group ?? undefined) !== group) {
      group = l.group ?? undefined
      if (group) out.push(`${group}:`)
    }
    out.push(l.raw)
  }
  return out
}
