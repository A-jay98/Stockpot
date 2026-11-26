import { ApiError } from '../../api/problem'
import { isCancelled } from './webauthn'

/** A message for a failed sign-in or enrolment, or undefined when the user just cancelled. */
export function authErrorMessage(e: unknown): string | undefined {
  if (isCancelled(e)) return undefined
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return 'Something went wrong. Try again.'
}
