import { useSyncExternalStore } from 'react'

// "Colourful food icons": per device for M1 (localStorage), default off.
const STORAGE_KEY = 'stockpot.colourfulFoodIcons'
const listeners = new Set<() => void>()

function read(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

let current = read()

export function setColourfulIcons(on: boolean) {
  current = on
  try {
    if (on) localStorage.setItem(STORAGE_KEY, '1')
    else localStorage.removeItem(STORAGE_KEY)
  } catch {
    // Storage blocked: the choice lasts for this session only.
  }
  for (const l of listeners) l()
}

export function useColourfulIcons(): [boolean, (on: boolean) => void] {
  const on = useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => current,
    () => current,
  )
  return [on, setColourfulIcons]
}
