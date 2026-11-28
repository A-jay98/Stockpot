import 'fake-indexeddb/auto'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { dispatch, pendingCommands } from '../../offline/outbox'
import { renderApp } from '../../test-utils'
import type { ChatMessage } from './chat'

const R = '0190a1b2-0000-7000-8000-00000000c001'
const V1 = '0190a1b2-0000-7000-8000-00000000c0a1'

const msg = (over: Partial<ChatMessage>): ChatMessage => ({
  id: crypto.randomUUID(),
  recipeId: R,
  versionId: V1,
  versionN: 1,
  role: 'assistant',
  text: '',
  proposalState: 'none',
  createdAt: '2026-11-27T10:00:00Z',
  ...over,
})

const proposal = { changeSummary: '2 tbsp tomato paste', ingredients: ['2 tbsp tomato paste'] }

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  })

let chat: ChatMessage[] = []
let onPost: (text: string) => Response

beforeEach(async () => {
  chat = []
  onPost = () => json({ messages: [] })
  vi.stubGlobal('fetch', async (req: Request) => {
    const url = new URL(req.url)
    if (url.pathname === `/api/v1/recipes/${R}/chat`) {
      if (req.method === 'GET') return json({ messages: chat })
      const { text } = (await req.json()) as { text: string }
      return onPost(text)
    }
    const dismiss = url.pathname.match(/\/chat\/([^/]+)\/dismiss$/)
    if (dismiss) {
      const m = chat.find((x) => x.id === dismiss[1])
      return json({ ...m, proposalState: 'dismissed' })
    }
    throw new TypeError('offline') // sync stays offline; the outbox keeps commands
  })
  await db.meta.put({ key: 'sync.household', value: '0190a1b2-0000-7000-8000-0000000000a1' })
  await dispatch('recipe.create', {
    id: R,
    versionId: V1,
    title: 'Gheymeh',
    ingredients: ['1 tbsp tomato paste'],
    steps: [{ text: 'Fry the paste.' }],
  })
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await Promise.all(db.tables.map((t) => t.clear()))
})

test('a question gets an answer without a proposal', async () => {
  onPost = (text) =>
    json({
      messages: [msg({ role: 'user', text }), msg({ text: 'Yes, up to 3 months.' })],
    })
  renderApp(`/recipes/${R}`)
  const input = await screen.findByRole('textbox', { name: 'Message' })
  await userEvent.type(input, 'Can I freeze this?{Enter}')
  expect(await screen.findByText('Yes, up to 3 months.')).toBeInTheDocument()
  expect(screen.getByText('Can I freeze this?')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Save as/ })).not.toBeInTheDocument()
  expect(input).toHaveValue('')
})

test('Save as vN applies the change to the current version and waits for the server', async () => {
  const m = msg({ text: "I'd use 2 tbsp.", proposal, proposalState: 'pending' })
  chat = [msg({ role: 'user', text: 'Used 2 tbsp' }), m]
  renderApp(`/recipes/${R}`)
  await userEvent.click(await screen.findByRole('button', { name: 'Save as v2' }))
  expect(await screen.findByText('Saving as v2…')).toBeInTheDocument()
  expect(screen.queryByText(/Saved as/)).not.toBeInTheDocument()
  const [cmd] = (await pendingCommands()).filter((c) => c.type === 'recipe.version.create')
  expect(cmd?.payload).toMatchObject({
    recipeId: R,
    origin: 'chat',
    parentVersionId: V1,
    fromMessageId: m.id,
    changeSummary: '2 tbsp tomato paste',
    title: 'Gheymeh',
    ingredients: ['2 tbsp tomato paste'],
    steps: [{ text: 'Fry the paste.' }],
  })
  // The server applies it and marks the message saved.
  chat = [chat[0] as ChatMessage, { ...m, proposalState: 'saved', savedVersionN: 2 }]
  await db.outbox
    .where('id')
    .equals(cmd?.id ?? '')
    .delete()
  expect(await screen.findByText('Saved as v2.')).toBeInTheDocument()
})

test('a proposal on an older version applies on top of later edits; Not now sets it aside', async () => {
  const v2 = '0190a1b2-0000-7000-8000-00000000c0a2'
  await dispatch('recipe.version.create', {
    recipeId: R,
    versionId: v2,
    origin: 'manual',
    title: 'Gheymeh',
    ingredients: ['For the stew:', '1 tbsp tomato paste', '1 onion'],
    steps: [],
  })
  chat = [
    msg({
      text: 'For two.',
      proposal: { changeSummary: 'For two', servings: 2 },
      proposalState: 'pending',
    }),
    msg({ text: 'Try lamb.', proposal, proposalState: 'pending' }),
  ]
  renderApp(`/recipes/${R}`)
  const [first] = await screen.findAllByRole('button', { name: 'Save as v3 (based on v1)' })
  await userEvent.click(first as HTMLElement)
  const [cmd] = (await pendingCommands()).filter(
    (c) =>
      c.type === 'recipe.version.create' && (c.payload as { origin: string }).origin === 'chat',
  )
  expect(cmd?.payload).toMatchObject({
    parentVersionId: v2,
    servings: 2,
    ingredients: ['For the stew:', '1 tbsp tomato paste', '1 onion'],
  })
  expect(await screen.findByText('Saving as v3…')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Not now' }))
  expect(await screen.findByText('Not saved')).toBeInTheDocument()
})

test('a change that breaks a recipe rule says why there is nothing to save', async () => {
  chat = [msg({ text: 'Scaled to 1000.', proposalError: 'servings must be 1-100' })]
  renderApp(`/recipes/${R}`)
  expect(
    await screen.findByText(/Nothing to save as a version: servings must be 1-100/),
  ).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Save as/ })).not.toBeInTheDocument()
})

test('when the assistant is unavailable it offers the editor and keeps the text', async () => {
  onPost = () =>
    json(
      {
        type: 'about:blank',
        title: 'Service Unavailable',
        status: 503,
        code: 'llm_unavailable',
      },
      503,
    )
  renderApp(`/recipes/${R}`)
  const input = await screen.findByRole('textbox', { name: 'Message' })
  await userEvent.type(input, 'Make it spicier{Enter}')
  const link = await screen.findByRole('link', { name: 'Edit the recipe' })
  expect(link).toHaveAttribute('href', `/recipes/${R}/edit`)
  expect(input).toHaveValue('Make it spicier')
})

test('offline, the input is disabled with a note', async () => {
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
  renderApp(`/recipes/${R}`)
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Message' })).toBeDisabled())
  expect(screen.getByText(/The chat needs a connection/)).toBeInTheDocument()
  vi.restoreAllMocks()
})
