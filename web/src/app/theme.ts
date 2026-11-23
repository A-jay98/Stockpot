import { useSyncExternalStore } from 'react'

/** `system` follows prefers-color-scheme; `light`/`dark` pin it via `<html data-theme>`. */
export type ThemePreference = 'system' | 'light' | 'dark'

const STORAGE_KEY = 'stockpot.theme'
const listeners = new Set<() => void>()

function read(): ThemePreference {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

let current: ThemePreference = 'system'

function apply(pref: ThemePreference) {
  const root = document.documentElement
  if (pref === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', pref)
}

/** Call before the first render to avoid a flash of the wrong theme. */
export function initTheme() {
  current = read()
  apply(current)
}

export function setTheme(pref: ThemePreference) {
  current = pref
  try {
    if (pref === 'system') localStorage.removeItem(STORAGE_KEY)
    else localStorage.setItem(STORAGE_KEY, pref)
  } catch {
    // Storage blocked (private mode): the choice lasts for this session only.
  }
  apply(pref)
  for (const l of listeners) l()
}

export function useTheme(): [ThemePreference, (pref: ThemePreference) => void] {
  const pref = useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => current,
    () => current,
  )
  return [pref, setTheme]
}
