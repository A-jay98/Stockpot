import { useEffect, useState } from 'react'
import { api } from './api/client'

type Status = { kind: 'loading' } | { kind: 'ok'; version: string } | { kind: 'down' }

export function App() {
  const [status, setStatus] = useState<Status>({ kind: 'loading' })

  useEffect(() => {
    let live = true
    api
      .GET('/health')
      .then(({ data }) => {
        if (live) setStatus(data ? { kind: 'ok', version: data.version } : { kind: 'down' })
      })
      .catch(() => {
        if (live) setStatus({ kind: 'down' })
      })
    return () => {
      live = false
    }
  }, [])

  return (
    <main className="shell">
      <h1>Stockpot</h1>
      <p className="tagline">Your household's fridge, pantry, shopping and recipes.</p>
      <p className="status" role="status">
        {status.kind === 'loading' && 'Connecting…'}
        {status.kind === 'ok' && `Server ${status.version}`}
        {status.kind === 'down' && 'Server unreachable'}
      </p>
    </main>
  )
}
