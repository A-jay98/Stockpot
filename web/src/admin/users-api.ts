import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { adminKeys } from './api'

type S = components['schemas']
export type AdminUser = S['AdminUser']
export type UserStatus = S['UserStatus']
export type Invite = S['Invite']
export type InviteCreate = S['InviteCreate']
export type Household = S['Household']
export type Session = S['Session']

export const userKeys = {
  users: () => [...adminKeys.all, 'users'] as const,
  sessions: (userId: string) => [...adminKeys.all, 'users', userId, 'sessions'] as const,
  invites: () => [...adminKeys.all, 'invites'] as const,
  households: () => [...adminKeys.all, 'households'] as const,
}

export function useUsers() {
  return useQuery({
    queryKey: userKeys.users(),
    queryFn: () => unwrap(api.GET('/v1/admin/users')),
  })
}

export function useUserSessions(userId: string, enabled: boolean) {
  return useQuery({
    queryKey: userKeys.sessions(userId),
    queryFn: () =>
      unwrap(api.GET('/v1/admin/users/{userId}/sessions', { params: { path: { userId } } })),
    enabled,
  })
}

export function useInvites() {
  return useQuery({
    queryKey: userKeys.invites(),
    queryFn: () => unwrap(api.GET('/v1/invites', { params: { query: { all: true } } })),
  })
}

export function useHouseholds() {
  return useQuery({
    queryKey: userKeys.households(),
    queryFn: () => unwrap(api.GET('/v1/admin/households')),
    staleTime: 5 * 60_000,
  })
}

/** Every user mutation refreshes the user list (and the audit views). */
function useUserMutation<V, R>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSettled: () => qc.invalidateQueries({ queryKey: adminKeys.all }),
  })
}

export const useApproveUser = () =>
  useUserMutation((userId: string) =>
    unwrap(api.POST('/v1/admin/users/{userId}/approve', { params: { path: { userId } } })),
  )

export const useRejectUser = () =>
  useUserMutation((userId: string) =>
    unwrap(api.POST('/v1/admin/users/{userId}/reject', { params: { path: { userId } } })),
  )

export const useUpdateUser = () =>
  useUserMutation(({ userId, ...body }: { userId: string } & S['AdminUserUpdate']) =>
    unwrap(api.PATCH('/v1/admin/users/{userId}', { params: { path: { userId } }, body })),
  )

export const useRevokeSession = () =>
  useUserMutation((sessionId: string) =>
    unwrap(api.DELETE('/v1/admin/sessions/{sessionId}', { params: { path: { sessionId } } })),
  )

export const useCreateInvite = () =>
  useUserMutation((body: InviteCreate) => unwrap(api.POST('/v1/invites', { body })))

export const useRevokeInvite = () =>
  useUserMutation((inviteId: string) =>
    unwrap(api.DELETE('/v1/invites/{inviteId}', { params: { path: { inviteId } } })),
  )
