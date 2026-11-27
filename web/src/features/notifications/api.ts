import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../../api/client'
import type { components } from '../../api/schema'

export type Notification = components['schemas']['Notification']
export type Preferences = components['schemas']['NotificationPreferences']
export type PreferencesUpdate = components['schemas']['NotificationPreferencesUpdate']
export type TelegramConnection = components['schemas']['TelegramConnection']

export const notificationKeys = {
  inbox: ['notifications', 'inbox'] as const,
  prefs: ['notifications', 'prefs'] as const,
  telegram: ['notifications', 'telegram'] as const,
}

/** The inbox, refreshed every minute and when the app regains focus. */
export function useInbox(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.inbox,
    queryFn: () => unwrap(api.GET('/v1/notifications', { params: { query: { limit: 50 } } })),
    refetchInterval: 60_000,
    enabled,
  })
}

export function useMarkRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (ids?: string[]) =>
      unwrap(api.POST('/v1/notifications/read', { body: ids ? { ids } : {} })),
    onSettled: () => qc.invalidateQueries({ queryKey: notificationKeys.inbox }),
  })
}

export function usePreferences() {
  return useQuery({
    queryKey: notificationKeys.prefs,
    queryFn: () => unwrap(api.GET('/v1/notifications/preferences')),
  })
}

export function useSavePreferences() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: PreferencesUpdate) =>
      unwrap(api.PUT('/v1/notifications/preferences', { body })),
    onSuccess: (data) => qc.setQueryData(notificationKeys.prefs, data),
  })
}

/** Polls while a connect is pending, so the page flips to "connected" by itself. */
export function useTelegram(pending = false) {
  return useQuery({
    queryKey: notificationKeys.telegram,
    queryFn: () => unwrap(api.GET('/v1/notifications/telegram')),
    refetchInterval: pending ? 3_000 : false,
  })
}

export function useStartTelegramLink() {
  return useMutation({ mutationFn: () => unwrap(api.POST('/v1/notifications/telegram/link')) })
}

export function useDisconnectTelegram() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.DELETE('/v1/notifications/telegram')),
    onSettled: () => qc.invalidateQueries({ queryKey: notificationKeys.telegram }),
  })
}

export function useTelegramTest() {
  return useMutation({ mutationFn: () => unwrap(api.POST('/v1/notifications/telegram/test')) })
}
