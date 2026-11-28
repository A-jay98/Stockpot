import { SendHorizontal } from 'lucide-react'
import { type FormEvent, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../../../api/problem'
import { useOnline } from '../../../pwa/useOnline'
import { Button, cn, Input, toast } from '../../../ui'
import {
  type ChatMessage,
  saveProposal,
  useCommandState,
  useDismissProposal,
  usePostChat,
  useRecipeChat,
  useRefreshChat,
} from '../chat'
import { useRecipeVersions } from '../hooks'

/**
 * "Ask about this recipe": a chat that answers questions and proposes new versions
 * (architecture-m2 §10). Online only; offline the input is disabled with a note.
 */
export function ChatSection({ recipeId, versionId }: { recipeId: string; versionId: string }) {
  const online = useOnline()
  const chat = useRecipeChat(recipeId, online)
  const post = usePostChat(recipeId)
  const versions = useRecipeVersions(recipeId)
  const nextN = Math.max(0, ...versions.map((v) => v.n)) + 1
  const [text, setText] = useState('')

  const send = (e: FormEvent) => {
    e.preventDefault()
    const t = text.trim()
    if (!t || post.isPending) return
    post.mutate(t, { onSuccess: () => setText('') })
  }

  const messages = chat.data?.messages ?? []
  const unavailable = post.error instanceof ApiError && post.error.code === 'llm_unavailable'
  return (
    <section aria-label="Ask about this recipe" className="flex flex-col gap-2">
      <h2 className="t-label m-0 text-ink-muted">Ask about this recipe</h2>
      <div className="flex flex-col gap-2.5 rounded-lg bg-surface p-4 shadow-card">
        {messages.length === 0 && !post.isPending ? (
          <p className="t-body-sm m-0 text-ink-muted">
            Ask anything, or tell it what you changed ("I used 2 tbsp tomato paste and it was
            richer"). It can save the change as a new version.
          </p>
        ) : null}
        {messages.map((m) => (
          <Bubble
            key={m.id}
            message={m}
            recipeId={recipeId}
            currentVersionId={versionId}
            nextN={nextN}
          />
        ))}
        {post.isPending ? (
          <>
            <div className={cn(bubble, mine)}>{post.variables}</div>
            <div className={cn(bubble, theirs, 'text-ink-muted')} aria-live="polite">
              Thinking…
            </div>
          </>
        ) : null}
        {unavailable ? (
          <p className="t-body-sm m-0 text-ink-muted" role="status">
            The assistant is unavailable right now.{' '}
            <Link to={`/recipes/${recipeId}/edit`} className="font-semibold text-brand">
              Edit the recipe
            </Link>{' '}
            to make the change yourself.
          </p>
        ) : post.isError ? (
          <p className="t-body-sm m-0 text-expired" role="status">
            Couldn't send that. Try again.
          </p>
        ) : null}
        {!online ? (
          <p className="t-body-sm m-0 text-ink-muted" role="status">
            You're offline. The chat needs a connection; the recipe still works.
          </p>
        ) : null}
        <form onSubmit={send} className="mt-1 flex items-center gap-2">
          <Input
            aria-label="Message"
            placeholder="Tell it what you changed, or ask"
            value={text}
            maxLength={2000}
            disabled={!online || post.isPending}
            onChange={(e) => setText(e.target.value)}
          />
          <Button
            type="submit"
            size="icon"
            aria-label="Send"
            disabled={!online || post.isPending || !text.trim()}
          >
            <SendHorizontal aria-hidden />
          </Button>
        </form>
      </div>
    </section>
  )
}

const bubble = 't-body-sm max-w-[88%] rounded-[14px] px-3 py-2.5 whitespace-pre-line'
const mine = 'self-end rounded-br-[4px] bg-brand text-on-brand'
const theirs = 'self-start rounded-bl-[4px] bg-surface-2 text-ink'

function Bubble({
  message: m,
  recipeId,
  currentVersionId,
  nextN,
}: {
  message: ChatMessage
  recipeId: string
  currentVersionId: string
  nextN: number
}) {
  if (m.role === 'user') return <div className={cn(bubble, mine)}>{m.text}</div>
  return (
    <div className={cn(bubble, theirs)}>
      {m.text}
      {m.proposalError ? (
        <span className="mt-2 block text-[12.5px] text-ink-muted">
          Nothing to save as a version: {m.proposalError}.{' '}
          <Link to={`/recipes/${recipeId}/edit`} className="font-semibold text-brand">
            Edit the recipe
          </Link>
        </span>
      ) : null}
      {m.proposal ? (
        <ProposalActions
          message={m}
          recipeId={recipeId}
          currentVersionId={currentVersionId}
          nextN={nextN}
        />
      ) : null}
    </div>
  )
}

function ProposalActions({
  message: m,
  recipeId,
  currentVersionId,
  nextN,
}: {
  message: ChatMessage
  recipeId: string
  currentVersionId: string
  nextN: number
}) {
  const dismiss = useDismissProposal(recipeId)
  const refresh = useRefreshChat(recipeId)
  const online = useOnline()
  const [commandId, setCommandId] = useState<string>()
  const [savingN, setSavingN] = useState(nextN)
  const [busy, setBusy] = useState(false) // set synchronously: a double tap queues one version
  const command = useCommandState(commandId)
  // Once the server applied the command, it has marked the message saved: fetch that state.
  useEffect(() => {
    if (commandId && command === 'done') void refresh()
  }, [commandId, command, refresh])

  if (m.proposalState === 'saved') {
    return (
      <b className="mt-1 block">{m.savedVersionN ? `Saved as v${m.savedVersionN}.` : 'Saved.'}</b>
    )
  }
  if (commandId && command !== 'rejected') {
    return (
      <span className="mt-1 block text-[12.5px] text-ink-muted" role="status">
        {online ? `Saving as v${savingN}…` : "Saving when you're back online…"}
      </span>
    )
  }
  const stale = m.versionId !== currentVersionId
  const label = `Save as v${nextN}${stale ? ` (based on v${m.versionN})` : ''}`
  const onSave = async () => {
    if (busy) return
    setBusy(true)
    try {
      setSavingN(nextN)
      setCommandId(await saveProposal(recipeId, m))
    } catch {
      toast({ title: "Couldn't save the version", tone: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="mt-2 flex flex-col gap-2">
      <span className="text-[12.5px] text-ink-muted">{m.proposal?.changeSummary}</span>
      {command === 'rejected' && commandId ? (
        <span className="text-[12.5px] text-expired" role="status">
          The server didn't save that version.
        </span>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={onSave} disabled={busy}>
          {label}
        </Button>
        {m.proposalState === 'pending' ? (
          <Button
            size="sm"
            variant="secondary"
            className="bg-surface"
            disabled={!online || dismiss.isPending}
            onClick={() =>
              dismiss.mutate(m.id, {
                onError: () => toast({ title: "Couldn't do that. Try again.", tone: 'error' }),
              })
            }
          >
            Not now
          </Button>
        ) : (
          <span className="self-center text-[12.5px] text-ink-muted">Not saved</span>
        )}
      </div>
    </div>
  )
}
