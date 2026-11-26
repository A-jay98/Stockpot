import { Check, Copy, Plus, UserCheck, UserX, X } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { ApiError } from '../api/problem'
import { useMe } from '../features/auth/session'
import {
  Badge,
  type BadgeProps,
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogTrigger,
  Field,
  Input,
  Segmented,
  toast,
} from '../ui'
import { AdminPage, ErrorNote, formatTime, Table } from './ui'
import {
  type AdminUser,
  type Invite,
  type UserStatus,
  useApproveUser,
  useCreateInvite,
  useHouseholds,
  useInvites,
  useRejectUser,
  useRevokeInvite,
  useRevokeSession,
  useUpdateUser,
  useUserSessions,
  useUsers,
} from './users-api'

const statusTone: Record<UserStatus, BadgeProps['tone']> = {
  pending: 'soon',
  active: 'fresh',
  disabled: 'neutral',
}

const inviteTone: Record<Invite['status'], BadgeProps['tone']> = {
  pending: 'brand',
  accepted: 'fresh',
  revoked: 'neutral',
  expired: 'neutral',
}

const errorText = (e: unknown) =>
  e instanceof ApiError ? e.message : 'Could not reach the server. Check your connection and retry.'

const failed = (title: string) => (e: unknown) =>
  toast({ title, description: errorText(e), tone: 'error' })

export function UsersPage() {
  const users = useUsers()
  const all = users.data ?? []
  const pending = all.filter((u) => u.status === 'pending')
  const others = all.filter((u) => u.status !== 'pending')

  return (
    <AdminPage
      title="Users"
      intro="Approve sign-ups, invite people, change roles and end sessions. Every change is in the audit log."
    >
      {users.error && <ErrorNote error={users.error} />}
      {pending.length > 0 && (
        <Section title={`Waiting for approval (${pending.length})`}>
          <ul className="m-0 grid list-none gap-3 p-0 sm:grid-cols-[repeat(auto-fill,minmax(260px,1fr))]">
            {pending.map((u) => (
              <PendingCard key={u.id} user={u} />
            ))}
          </ul>
        </Section>
      )}
      <Section title="Users">
        <UsersTable users={others} loading={users.isPending} />
      </Section>
      <InvitesSection />
    </AdminPage>
  )
}

function Section({
  title,
  actions,
  children,
}: {
  title: string
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <div className="flex min-h-8 items-center gap-2">
        <h2 className="t-label m-0 flex-1 text-ink-muted">{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  )
}

function PendingCard({ user }: { user: AdminUser }) {
  const approve = useApproveUser()
  const reject = useRejectUser()
  const busy = approve.isPending || reject.isPending
  return (
    <li className="flex flex-col gap-3 rounded-lg bg-surface p-3.5 shadow-card">
      <div className="flex flex-col gap-0.5">
        <span className="font-semibold">{user.displayName}</span>
        <span className="text-xs text-ink-muted">Signed up {formatTime(user.createdAt)}</span>
      </div>
      <div className="flex gap-2">
        <Button
          size="sm"
          disabled={busy}
          aria-label={`Approve ${user.displayName}`}
          onClick={() =>
            approve.mutate(user.id, {
              onSuccess: () => toast({ title: `Approved ${user.displayName}`, tone: 'success' }),
              onError: failed(`Could not approve ${user.displayName}`),
            })
          }
        >
          <UserCheck aria-hidden />
          Approve
        </Button>
        <Confirm
          title={`Reject ${user.displayName}?`}
          description="They will not be able to sign in. You can enable the account later from the users list."
          confirm="Reject"
          onConfirm={() =>
            reject.mutate(user.id, {
              onSuccess: () => toast({ title: `Rejected ${user.displayName}`, tone: 'success' }),
              onError: failed(`Could not reject ${user.displayName}`),
            })
          }
        >
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            aria-label={`Reject ${user.displayName}`}
          >
            <UserX aria-hidden />
            Reject
          </Button>
        </Confirm>
      </div>
    </li>
  )
}

/** A destructive step behind a confirmation dialog. */
function Confirm({
  title,
  description,
  confirm,
  onConfirm,
  children,
}: {
  title: string
  description: string
  confirm: string
  onConfirm: () => void
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{children}</DialogTrigger>
      <DialogContent title={title} description={description}>
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            variant="danger"
            onClick={() => {
              setOpen(false)
              onConfirm()
            }}
          >
            {confirm}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function UsersTable({ users, loading }: { users: AdminUser[]; loading: boolean }) {
  const me = useMe().data
  const [managing, setManaging] = useState<string>()
  const current = users.find((u) => u.id === managing)
  return (
    <>
      <Table aria-label="Users">
        <thead>
          <tr>
            <th>Name</th>
            <th>Role</th>
            <th>Status</th>
            <th>Households</th>
            <th>Last sign-in</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {users.length === 0 && !loading && (
            <tr>
              <td colSpan={6} className="text-ink-muted">
                No users yet.
              </td>
            </tr>
          )}
          {users.map((u) => (
            <tr key={u.id}>
              <td className="font-semibold">
                {u.displayName}
                {u.id === me?.user.id && <span className="font-normal text-ink-muted"> (you)</span>}
              </td>
              <td>
                <Badge tone={u.systemRole === 'admin' ? 'brand' : 'neutral'}>{u.systemRole}</Badge>
              </td>
              <td>
                <Badge tone={statusTone[u.status]}>{u.status}</Badge>
              </td>
              <td className="text-ink-muted">
                {u.households.length === 0
                  ? '—'
                  : u.households
                      .map((h) => (h.role === 'owner' ? `${h.name} (owner)` : h.name))
                      .join(', ')}
              </td>
              <td className="whitespace-nowrap">{formatTime(u.lastLoginAt)}</td>
              <td className="text-right">
                <Button
                  size="sm"
                  variant="secondary"
                  aria-label={`Manage ${u.displayName}`}
                  onClick={() => setManaging(u.id)}
                >
                  Manage
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      <Dialog open={!!current} onOpenChange={(o) => !o && setManaging(undefined)}>
        {current && <ManageUser user={current} self={current.id === me?.user.id} />}
      </Dialog>
    </>
  )
}

function ManageUser({ user, self }: { user: AdminUser; self: boolean }) {
  const update = useUpdateUser()
  const sessions = useUserSessions(user.id, true)
  const revoke = useRevokeSession()
  const [error, setError] = useState<string>()
  const change = (patch: { status?: 'active' | 'disabled'; systemRole?: 'admin' | 'user' }) => {
    setError(undefined)
    update.mutate(
      { userId: user.id, ...patch },
      {
        onSuccess: (u) => toast({ title: `Updated ${u.displayName}`, tone: 'success' }),
        onError: (e) => setError(errorText(e)),
      },
    )
  }
  const disabled = user.status === 'disabled'

  return (
    <DialogContent
      title={user.displayName}
      description={`Joined ${formatTime(user.createdAt)}${user.approvedAt ? ` · approved ${formatTime(user.approvedAt)}` : ''}`}
      className="max-w-[560px]"
    >
      {error && (
        <p role="alert" className="m-0 rounded-md bg-expired-bg px-3 py-2 text-[14px] text-expired">
          {error}
        </p>
      )}
      <div className="flex flex-col gap-1.5">
        <span className="text-[13px] font-semibold text-ink-muted">System role</span>
        <Segmented
          aria-label="System role"
          className="sm:w-64"
          value={user.systemRole}
          onValueChange={(systemRole) => change({ systemRole })}
          options={[
            { value: 'user', label: 'User' },
            { value: 'admin', label: 'Admin' },
          ]}
        />
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex-1 text-[14px] text-ink-muted">
          {disabled
            ? 'Disabled: cannot sign in.'
            : self
              ? 'This is your account.'
              : 'Disabling signs them out everywhere.'}
        </span>
        {disabled ? (
          <Button
            size="sm"
            disabled={update.isPending}
            onClick={() => change({ status: 'active' })}
          >
            Enable account
          </Button>
        ) : (
          <Confirm
            title={`Disable ${user.displayName}?`}
            description="They are signed out of every device and cannot sign in until enabled again."
            confirm="Disable"
            onConfirm={() => change({ status: 'disabled' })}
          >
            <Button size="sm" variant="danger" disabled={update.isPending || self}>
              Disable account
            </Button>
          </Confirm>
        )}
      </div>

      <section aria-label="Sessions" className="flex flex-col gap-2">
        <h3 className="t-label m-0 text-ink-muted">Sessions</h3>
        {sessions.error && <ErrorNote error={sessions.error} />}
        {sessions.data?.length === 0 && (
          <p className="m-0 text-[14px] text-ink-muted">No active sessions.</p>
        )}
        <ul className="m-0 flex list-none flex-col gap-0 rounded-lg border border-border p-0 [&>li+li]:border-t [&>li+li]:border-border">
          {sessions.data?.map((s) => (
            <li key={s.id} className="flex items-center gap-3 px-3 py-2.5">
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="truncate text-[14px]" title={s.userAgent}>
                  {s.userAgent || 'Unknown device'}
                  {s.current && <Badge className="ml-2">this session</Badge>}
                </span>
                <span className="text-xs text-ink-muted">
                  {s.ip ? `${s.ip} · ` : ''}last seen {formatTime(s.lastSeenAt)} · expires{' '}
                  {formatTime(s.expiresAt)}
                </span>
              </div>
              <Button
                size="sm"
                variant="ghost"
                disabled={revoke.isPending}
                aria-label={`Revoke session ${s.id}`}
                onClick={() =>
                  revoke.mutate(s.id, {
                    onSuccess: () => toast({ title: 'Session revoked', tone: 'success' }),
                    onError: failed('Could not revoke the session'),
                  })
                }
              >
                <X aria-hidden />
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      </section>
    </DialogContent>
  )
}

function InvitesSection() {
  const invites = useInvites()
  const revoke = useRevokeInvite()
  const [creating, setCreating] = useState(false)
  const rows = invites.data ?? []
  return (
    <Section
      title="Invites"
      actions={
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus aria-hidden />
          Create invite
        </Button>
      }
    >
      {invites.error && <ErrorNote error={invites.error} />}
      <Table aria-label="Invites">
        <thead>
          <tr>
            <th>Household</th>
            <th>Role</th>
            <th>Note</th>
            <th>Created by</th>
            <th>Expires</th>
            <th>Status</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && !invites.isPending && (
            <tr>
              <td colSpan={7} className="text-ink-muted">
                No invites yet.
              </td>
            </tr>
          )}
          {rows.map((i) => (
            <tr key={i.id}>
              <td className="font-semibold">{i.householdName}</td>
              <td>{i.role}</td>
              <td className="max-w-[28ch] truncate text-ink-muted" title={i.note}>
                {i.note || '—'}
              </td>
              <td>{i.createdByName}</td>
              <td className="whitespace-nowrap">{formatTime(i.expiresAt)}</td>
              <td>
                <Badge tone={inviteTone[i.status]}>{i.status}</Badge>
              </td>
              <td className="text-right">
                {i.status === 'pending' && (
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={revoke.isPending}
                    aria-label={`Revoke invite to ${i.householdName}`}
                    onClick={() =>
                      revoke.mutate(i.id, {
                        onSuccess: () => toast({ title: 'Invite revoked', tone: 'success' }),
                        onError: failed('Could not revoke the invite'),
                      })
                    }
                  >
                    <X aria-hidden />
                    Revoke
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      <Dialog open={creating} onOpenChange={setCreating}>
        {creating && <CreateInvite />}
      </Dialog>
    </Section>
  )
}

function CreateInvite() {
  const households = useHouseholds()
  const create = useCreateInvite()
  const [householdId, setHouseholdId] = useState('')
  const [role, setRole] = useState<'member' | 'owner'>('member')
  const [note, setNote] = useState('')
  const [copied, setCopied] = useState(false)
  const chosen = householdId || households.data?.[0]?.id || ''

  if (create.data) {
    const { url } = create.data
    return (
      <DialogContent
        title="Invite ready"
        description="Share this link now. It is shown only once and works for one person."
      >
        <Field label="Invite link">
          {(a11y) => <Input {...a11y} readOnly value={url} onFocus={(e) => e.target.select()} />}
        </Field>
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            onClick={() =>
              void navigator.clipboard.writeText(url).then(
                () => setCopied(true),
                () => toast({ title: 'Copy failed; select the link and copy it', tone: 'error' }),
              )
            }
          >
            {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
            {copied ? 'Copied' : 'Copy link'}
          </Button>
          <DialogClose asChild>
            <Button>Done</Button>
          </DialogClose>
        </div>
      </DialogContent>
    )
  }

  return (
    <DialogContent
      title="Create invite"
      description="People who join with it still need approval unless that is turned off under Settings, Sign-in."
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate({ householdId: chosen || undefined, role, note: note.trim() || undefined })
        }}
      >
        {create.error && (
          <p
            role="alert"
            className="m-0 rounded-md bg-expired-bg px-3 py-2 text-[14px] text-expired"
          >
            {errorText(create.error)}
          </p>
        )}
        <Field label="Household">
          {(a11y) => (
            <select
              {...a11y}
              className="h-11 w-full rounded-md border border-border bg-surface px-3 text-base text-ink"
              value={chosen}
              onChange={(e) => setHouseholdId(e.target.value)}
            >
              {households.data?.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name} ({h.memberCount} {h.memberCount === 1 ? 'member' : 'members'})
                </option>
              ))}
            </select>
          )}
        </Field>
        <div className="flex flex-col gap-1.5">
          <span className="text-[13px] font-semibold text-ink-muted">Role</span>
          <Segmented
            aria-label="Role"
            value={role}
            onValueChange={setRole}
            options={[
              { value: 'member', label: 'Member' },
              { value: 'owner', label: 'Owner' },
            ]}
          />
        </div>
        <Field label="Note" hint="Optional, e.g. who it is for. Shown in the invites list.">
          {(a11y) => (
            <Input
              {...a11y}
              maxLength={200}
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          )}
        </Field>
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </DialogClose>
          <Button type="submit" disabled={create.isPending || !chosen}>
            {create.isPending ? 'Creating…' : 'Create invite'}
          </Button>
        </div>
      </form>
    </DialogContent>
  )
}
