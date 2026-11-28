import { Camera, ImagePlus } from 'lucide-react'
import { type ChangeEvent, useRef, useState } from 'react'
import { savePhoto } from '../../lib/photos'
import { Button, cn, toast } from '../../ui'

export type PhotoPickerProps = {
  /** Called with the refs of the prepared photos, in the order picked. */
  onPicked: (refs: string[]) => void
  /** Allow several photos from the library at once (default true). */
  multiple?: boolean
  className?: string
}

const ACCEPT = 'image/jpeg,image/png,image/webp,image/heic,image/heif'

/**
 * Take a photo with the camera or pick from the library. Each photo is downscaled, stripped of
 * metadata and queued for upload on the device, so this works offline; `onPicked` gets refs to
 * use in commands straight away.
 */
export function PhotoPicker({ onPicked, multiple = true, className }: PhotoPickerProps) {
  const camera = useRef<HTMLInputElement>(null)
  const library = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)

  async function onChange(e: ChangeEvent<HTMLInputElement>) {
    const input = e.currentTarget
    const files = Array.from(input.files ?? [])
    input.value = '' // picking the same file again still fires change
    if (files.length === 0) return
    setBusy(true)
    const refs: string[] = []
    let failed = 0
    for (const file of files) {
      try {
        refs.push(await savePhoto(file))
      } catch {
        failed++
      }
    }
    setBusy(false)
    if (failed > 0) {
      toast({
        tone: 'error',
        title: failed === 1 ? "Couldn't read that photo" : `Couldn't read ${failed} photos`,
        description: 'Try a JPEG, PNG or WebP image.',
      })
    }
    if (refs.length > 0) onPicked(refs)
  }

  return (
    <div className={cn('flex gap-2', className)} aria-busy={busy}>
      <Button variant="secondary" disabled={busy} onClick={() => camera.current?.click()}>
        <Camera aria-hidden />
        Take photo
      </Button>
      <Button variant="secondary" disabled={busy} onClick={() => library.current?.click()}>
        <ImagePlus aria-hidden />
        {multiple ? 'Choose photos' : 'Choose photo'}
      </Button>
      <input
        ref={camera}
        type="file"
        accept={ACCEPT}
        capture="environment"
        hidden
        aria-label="Take photo"
        onChange={onChange}
      />
      <input
        ref={library}
        type="file"
        accept={ACCEPT}
        multiple={multiple}
        hidden
        aria-label={multiple ? 'Choose photos' : 'Choose photo'}
        onChange={onChange}
      />
    </div>
  )
}
