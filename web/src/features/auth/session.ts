import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, unwrap } from '../../api/client'
import { ApiError } from '../../api/problem'
import type { components } from '../../api/schema'
import { createPasskey, getPasskey } from './webauthn'

export type Me = components['schemas']['Me']
type CeremonyStart = components['schemas']['CeremonyStart']

export const meKey = ['me'] as const

/** The signed-in account, or null when there is no session. */
export function useMe() {
  return useQuery({
    queryKey: meKey,
    queryFn: async (): Promise<Me | null> => {
      try {
        return await unwrap(api.GET('/v1/me'))
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null
        throw e
      }
    },
    staleTime: 30_000,
  })
}

export function useSetupStatus() {
  return useQuery({ queryKey: ['setup'], queryFn: () => unwrap(api.GET('/v1/setup')) })
}

/** Runs a passkey enrolment (begin → browser prompt → finish) and stores the new session. */
export async function enroll(
  begin: () => Promise<CeremonyStart>,
  finish: (body: components['schemas']['CeremonyFinish']) => Promise<Me>,
) {
  const start = await begin()
  const credential = await createPasskey(start.options)
  return finish({ ceremonyId: start.ceremonyId, credential, name: deviceLabel() })
}

export async function signInWithPasskey(): Promise<Me> {
  const start = await unwrap(api.POST('/v1/auth/login/begin'))
  const credential = await getPasskey(start.options)
  return unwrap(
    api.POST('/v1/auth/login/finish', { body: { ceremonyId: start.ceremonyId, credential } }),
  )
}

/** Where a freshly signed-in user goes. */
export function homeFor(me: Me, next?: string | null) {
  if (me.user.status === 'pending') return '/pending'
  return next?.startsWith('/') && !next.startsWith('//') ? next : '/'
}

export function useSignOut() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/v1/auth/logout')),
    onSettled: () => {
      qc.clear()
      void navigate('/login', { replace: true })
    },
  })
}

export function useSwitchHousehold() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (householdId: string) =>
      unwrap(api.PUT('/v1/me/household', { body: { householdId } })),
    onSuccess: (me) => {
      // Everything household-scoped is stale now.
      qc.clear()
      qc.setQueryData(meKey, me)
    },
  })
}

/** A default passkey label from the platform, e.g. "iPhone" or "Mac". */
export function deviceLabel() {
  const ua = navigator.userAgent
  for (const [re, name] of [
    [/iPhone/, 'iPhone'],
    [/iPad/, 'iPad'],
    [/Android/, 'Android'],
    [/Mac OS X/, 'Mac'],
    [/Windows/, 'Windows'],
    [/Linux/, 'Linux'],
  ] as const) {
    if (re.test(ua)) return name
  }
  return 'Passkey'
}
