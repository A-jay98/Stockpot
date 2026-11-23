import { CloudOff } from 'lucide-react'
import { useOnline } from './useOnline'

/** Thin banner while the device is offline. Changes made offline will queue (M1). */
export function OfflineIndicator() {
  const online = useOnline()
  if (online) return null
  return (
    <div
      role="status"
      className="flex items-center justify-center gap-2 bg-soon-bg px-4 py-1.5 text-[13px] font-semibold text-soon [&_svg]:size-4"
    >
      <CloudOff aria-hidden />
      You're offline. Showing what's on this device.
    </div>
  )
}
