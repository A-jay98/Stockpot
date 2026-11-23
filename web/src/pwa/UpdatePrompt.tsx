import { useRegisterSW } from 'virtual:pwa-register/react'
import { useEffect } from 'react'
import { toast } from '../ui'

// Check for a new release hourly while the app stays open (deploys are webhook-driven).
const UPDATE_CHECK_MS = 60 * 60 * 1000

/** Registers the service worker and offers a reload when a new release is waiting. */
export function UpdatePrompt() {
  const {
    needRefresh: [needRefresh],
    updateServiceWorker,
  } = useRegisterSW({
    onRegisteredSW(_url, registration) {
      if (!registration) return
      setInterval(() => {
        if (navigator.onLine) void registration.update()
      }, UPDATE_CHECK_MS)
    },
  })

  useEffect(() => {
    if (!needRefresh) return
    toast({
      title: 'A new version of Stockpot is ready',
      duration: Number.POSITIVE_INFINITY,
      action: { label: 'Reload', onClick: () => void updateServiceWorker(true) },
    })
  }, [needRefresh, updateServiceWorker])

  return null
}
