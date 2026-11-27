import { useLiveQuery } from 'dexie-react-hooks'
import { type ComponentProps, useEffect, useState } from 'react'
import { blobUrl, type ThumbWidth } from '../../lib/photos'
import { db } from '../../offline/db'

export type BlobImageProps = Omit<ComponentProps<'img'>, 'src'> & {
  /** Blob ref (SHA-256) of the photo. */
  blobRef: string
  /** Thumbnail width; omit for the full photo. */
  w?: ThumbWidth
  alt: string
}

/**
 * A household photo. A photo taken on this device that is still waiting for upload is shown from
 * IndexedDB; otherwise it loads from the server (cached by the service worker, so it also shows
 * offline once seen). A missing thumbnail falls back to the full photo, which may be cached.
 */
export function BlobImage({ blobRef, w, alt, onError, ...img }: BlobImageProps) {
  const pending = useLiveQuery(() => db.pending_blob.get(blobRef), [blobRef], undefined)
  const [localUrl, setLocalUrl] = useState<string>()
  const [fallback, setFallback] = useState(false)

  useEffect(() => {
    if (!pending) {
      setLocalUrl(undefined)
      return
    }
    const url = URL.createObjectURL(pending.blob)
    setLocalUrl(url)
    return () => URL.revokeObjectURL(url)
  }, [pending])

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new photo or size retries the thumbnail.
  useEffect(() => setFallback(false), [blobRef, w])

  const src = localUrl ?? blobUrl(blobRef, fallback ? undefined : w)
  return (
    <img
      loading="lazy"
      decoding="async"
      {...img}
      alt={alt}
      src={src}
      onError={(e) => {
        if (!localUrl && w && !fallback) setFallback(true)
        onError?.(e)
      }}
    />
  )
}
