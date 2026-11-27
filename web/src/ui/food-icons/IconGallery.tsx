import { useMemo, useState } from 'react'
import { type ThemePreference, useTheme } from '../../app/theme'
import { Segmented } from '../segmented'
import { FoodIcon } from './FoodIcon'
import { iconRegistry } from './registry'

/** Dev-only gallery (`/dev/icons`): every registry icon in both styles; the theme toggle switches light and dark. */
export function IconGallery() {
  const [q, setQ] = useState('')
  const [theme, setTheme] = useTheme()
  const entries = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return Object.entries(iconRegistry).filter(
      ([k, e]) =>
        !needle ||
        k.includes(needle) ||
        e.label.toLowerCase().includes(needle) ||
        e.tags.some((t) => t.includes(needle)),
    )
  }, [q])
  return (
    <div className="flex flex-col gap-4 p-4">
      <h1 className="t-title m-0">Food icons ({entries.length})</h1>
      <input
        className="h-11 rounded-md border border-border bg-surface px-3"
        placeholder="Filter by key, label or tag"
        aria-label="Filter icons"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Segmented<ThemePreference>
        aria-label="Theme"
        value={theme}
        onValueChange={setTheme}
        options={[
          { value: 'light', label: 'Light' },
          { value: 'dark', label: 'Dark' },
        ]}
      />
      <ul className="m-0 grid list-none grid-cols-[repeat(auto-fill,minmax(84px,1fr))] gap-2 p-0">
        {entries.map(([k, e]) => (
          <li key={k} className="flex flex-col items-center gap-1 rounded-md bg-surface-2 p-2">
            <div className="flex gap-2">
              <FoodIcon k={k} variant="high-contrast" size={28} label={e.label} />
              <FoodIcon k={k} variant="flat" size={28} label={`${e.label} (colour)`} />
            </div>
            <code className="max-w-full truncate text-[10px] text-ink-muted">{k}</code>
          </li>
        ))}
      </ul>
    </div>
  )
}
