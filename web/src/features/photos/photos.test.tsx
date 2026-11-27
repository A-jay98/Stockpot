import { Blob as NodeBlob } from 'node:buffer'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { db } from '../../offline/db'
import { BlobImage } from './BlobImage'
import { PhotoPicker } from './PhotoPicker'

const savePhoto = vi.hoisted(() => vi.fn())
vi.mock('../../lib/photos', async (orig) => ({
  ...(await orig<typeof import('../../lib/photos')>()),
  savePhoto,
}))

const REF = 'a'.repeat(64)

beforeEach(() => {
  vi.stubGlobal(
    'URL',
    Object.assign(URL, { createObjectURL: () => 'blob:local', revokeObjectURL: () => {} }),
  )
})

afterEach(async () => {
  savePhoto.mockReset()
  await db.pending_blob.clear()
})

test('BlobImage shows a photo still waiting for upload from the device', async () => {
  await db.pending_blob.put({
    ref: REF,
    blob: new NodeBlob(['x'], { type: 'image/jpeg' }),
    createdAt: '',
  })
  render(<BlobImage blobRef={REF} w={512} alt="Curry" />)
  await waitFor(() => expect(screen.getByAltText('Curry')).toHaveAttribute('src', 'blob:local'))
})

test('BlobImage loads uploaded photos from the server, falling back to the full photo', async () => {
  render(<BlobImage blobRef={REF} w={256} alt="Curry" />)
  const img = screen.getByAltText('Curry')
  expect(img).toHaveAttribute('src', `/api/v1/blobs/${REF}?w=256`)
  fireEvent.error(img)
  expect(img).toHaveAttribute('src', `/api/v1/blobs/${REF}`)
})

test('PhotoPicker prepares each picked photo and reports the refs', async () => {
  savePhoto.mockImplementation(async (f: File) => {
    if (f.name === 'bad.png') throw new Error('decode')
    return `ref-${f.name}`
  })
  const onPicked = vi.fn()
  render(<PhotoPicker onPicked={onPicked} />)
  const files = [
    new File(['1'], 'a.jpg', { type: 'image/jpeg' }),
    new File(['2'], 'bad.png', { type: 'image/png' }),
    new File(['3'], 'b.jpg', { type: 'image/jpeg' }),
  ]
  await userEvent.upload(screen.getByLabelText('Choose photos'), files)
  await waitFor(() => expect(onPicked).toHaveBeenCalledWith(['ref-a.jpg', 'ref-b.jpg']))
  expect(screen.getByRole('button', { name: /Take photo/ })).toBeEnabled()
  expect(screen.getByLabelText('Take photo')).toHaveAttribute('capture', 'environment')
})
