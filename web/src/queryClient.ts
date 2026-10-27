import { QueryClient } from '@tanstack/react-query'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      // Pages only listen to WS events while mounted, and derived views (e.g.
      // the node page's per-workspace task counts) have no WS listener at all.
      // A cached entry from an earlier visit must therefore never be rendered
      // as current: every route mount re-reads the hub, even when the entry is
      // still inside a per-query staleTime window.
      refetchOnMount: 'always',
      staleTime: 0,
    },
  },
})
