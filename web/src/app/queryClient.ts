import { QueryClient } from '@tanstack/react-query'
import { ApiError } from '../api/problem'

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        // Client errors (4xx) won't fix themselves; retry network and 5xx failures only.
        retry: (count, error) => !(error instanceof ApiError && error.status < 500) && count < 2,
      },
    },
  })
}
