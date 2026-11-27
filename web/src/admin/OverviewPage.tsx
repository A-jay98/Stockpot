import {
  Activity,
  Database,
  DatabaseBackup,
  ListChecks,
  Rocket,
  ScrollText,
  Sparkles,
} from 'lucide-react'
import { Link } from 'react-router'
import { useOverview } from './api'
import { AdminPage, ErrorNote, formatBytes, formatDuration, formatTime, number, Tile } from './ui'

/** Job states that mean "waiting or working", i.e. the queue depth. */
const queued = ['available', 'scheduled', 'retryable', 'pending', 'running']

export function OverviewPage() {
  const { data: o, error, dataUpdatedAt } = useOverview({ poll: true })
  const intro = o
    ? `${o.database.reachable && o.database.schemaCurrent ? 'Everything is healthy.' : 'Something needs attention.'} Refreshes every 15 s${dataUpdatedAt ? `, last at ${new Date(dataUpdatedAt).toLocaleTimeString()}` : ''}.`
    : 'Loading…'

  const jobs = o?.jobs ?? {}
  const depth = queued.reduce((n, s) => n + (jobs[s] ?? 0), 0)
  const failed = jobs.discarded ?? 0
  const db = o?.database

  return (
    <AdminPage title="Overview" intro={intro}>
      {error && <ErrorNote error={error} />}
      {o && db && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
          <Tile
            icon={Rocket}
            label="Release"
            value={o.version}
            detail={
              <>
                <span className="font-mono">{o.commit.slice(0, 7) || 'unknown commit'}</span> · up{' '}
                {formatDuration(o.uptimeSeconds)}
              </>
            }
          />
          <Tile
            icon={Database}
            label="Database"
            value={!db.reachable ? 'Down' : db.schemaCurrent ? 'Healthy' : 'Migrations pending'}
            tone={!db.reachable ? 'expired' : db.schemaCurrent ? 'fresh' : 'soon'}
            detail={
              db.reachable && (
                <>
                  {db.latencyMs.toFixed(1)} ms
                  {db.sizeBytes !== undefined && ` · ${formatBytes(db.sizeBytes)}`}
                  {db.appVersion !== undefined && ` · schema v${db.appVersion}`}
                  {db.riverVersion !== undefined && ` · jobs v${db.riverVersion}`}
                </>
              )
            }
          />
          <Tile
            icon={ListChecks}
            label="Jobs"
            value={o.jobs ? `${number.format(depth)} queued` : '—'}
            tone={failed > 0 ? 'soon' : undefined}
            detail={
              o.jobs && (
                <Link to="/admin/jobs?state=discarded" className="text-inherit">
                  {number.format(failed)} failed (discarded)
                </Link>
              )
            }
          />
          <Tile
            icon={Activity}
            label="Errors (24 h)"
            value={o.errors24h === undefined ? '—' : number.format(o.errors24h)}
            tone={o.errors24h ? 'expired' : 'fresh'}
            detail={
              <Link to="/admin/logs?level=warn" className="text-inherit">
                {number.format(o.warnings24h ?? 0)} warnings
              </Link>
            }
          />
          {o.logSink && (
            <Tile
              icon={ScrollText}
              label="Log capture"
              value={o.logSink.dropped > 0 ? `${number.format(o.logSink.dropped)} dropped` : 'OK'}
              tone={o.logSink.dropped > 0 ? 'soon' : undefined}
              detail={`${number.format(o.logSink.written)} lines stored since start`}
            />
          )}
          {o.backups?.enabled && (
            <Tile
              icon={DatabaseBackup}
              label="Backups"
              value={o.backups.stale ? 'Needs attention' : 'OK'}
              tone={o.backups.stale ? 'expired' : 'fresh'}
              detail={
                <Link to="/admin/backups" className="text-inherit">
                  {o.backups.lastStatus === 'failed'
                    ? `Last run failed${o.backups.lastError ? `: ${o.backups.lastError.slice(0, 60)}` : ''}`
                    : o.backups.lastOkAt
                      ? `Last: ${formatTime(o.backups.lastOkAt)}`
                      : 'No successful backup yet'}
                </Link>
              }
            />
          )}
          {o.llm && (
            <Tile
              icon={Sparkles}
              label="AI today"
              value={
                o.llm.requestCap
                  ? `${number.format(o.llm.requests)} / ${number.format(o.llm.requestCap)}`
                  : number.format(o.llm.requests)
              }
              tone={o.llm.available ? undefined : 'soon'}
              detail={`${number.format(o.llm.tokens)} tokens${o.llm.available ? '' : ' · quota used, manual mode'}`}
            />
          )}
        </div>
      )}
    </AdminPage>
  )
}
