import { useLiveQuery } from 'dexie-react-hooks'
import { useEffect } from 'react'
import '../features/catalog/commands' // registers reducers for every command type
import { db } from './db'
import { adoptHousehold, startSyncEngine } from './sync'

/**
 * Keeps the replica in step with the server while a household is active: clears it when the
 * household changes, then syncs (outbox flush + pull) on SSE nudges, focus, reconnect and a timer.
 */
export function useReplicaSync(householdId: string | undefined) {
  useEffect(() => {
    if (!householdId) return
    let stop: (() => void) | undefined
    let cancelled = false
    void adoptHousehold(householdId).then(() => {
      if (!cancelled) stop = startSyncEngine()
    })
    return () => {
      cancelled = true
      stop?.()
    }
  }, [householdId])
}

/** Commands the server refused, for the "Couldn't sync" sheet. */
export function useRejectedCommands() {
  return useLiveQuery(() => db.outbox.where('status').equals('rejected').sortBy('seq'), [], [])
}
