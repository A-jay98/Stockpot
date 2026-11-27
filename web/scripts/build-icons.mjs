// Builds the food-icon sprites from the registry: `pnpm icons`.
// Output is committed and CI regenerates it (`make gen`), so it must be deterministic.
// Each sprite holds only registry keys as <symbol id="fi-<key>">, so there is no runtime CDN
// and no unused glyphs. Fluent comes in two styles (high-contrast mono, flat colour); the
// Tabler fallbacks are mono in both.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dir = join(root, 'src/ui/food-icons')
const registry = JSON.parse(readFileSync(join(dir, 'registry.json'), 'utf8'))

const load = (pkg) => {
  const data = JSON.parse(readFileSync(require.resolve(`@iconify-json/${pkg}/icons.json`), 'utf8'))
  return {
    width: data.width ?? 16,
    height: data.height ?? 16,
    get(name) {
      const alias = data.aliases?.[name]
      const icon = data.icons[alias ? alias.parent : name]
      if (!icon) return null
      return { width: data.width ?? 16, height: data.height ?? 16, ...icon }
    },
  }
}

const sets = {
  'high-contrast': { fe: load('fluent-emoji-high-contrast'), tb: load('tabler') },
  flat: { fe: load('fluent-emoji-flat'), tb: load('tabler') },
}

let failed = false
mkdirSync(join(dir, 'sprites'), { recursive: true })
for (const [style, byset] of Object.entries(sets)) {
  const symbols = []
  for (const [key, entry] of Object.entries(registry)) {
    const icon = byset[entry.set]?.get(entry.name)
    if (!icon) {
      console.error(`${style}: registry key "${key}" -> ${entry.set}:${entry.name} not found`)
      failed = true
      continue
    }
    symbols.push(
      `<symbol id="fi-${key}" viewBox="0 0 ${icon.width} ${icon.height}">${icon.body}</symbol>`,
    )
  }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" style="display:none">\n${symbols.join('\n')}\n</svg>\n`
  writeFileSync(join(dir, 'sprites', `${style}.svg`), svg)
  console.log(`${style}: ${symbols.length} icons, ${(svg.length / 1024).toFixed(0)} KB`)
}
if (failed) process.exit(1)
