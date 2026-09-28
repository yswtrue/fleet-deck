import type { ReactNode } from 'react'
import { Refine } from '@refinedev/core'
import type { ResourceProps } from '@refinedev/core'
import routerProvider from '@refinedev/react-router'
import { dataProvider } from '../dataProvider.ts'
import { queryClient } from '../queryClient.ts'

/**
 * Resources are registered so Refine knows the wire entities. The hub has no
 * RESTful CRUD surface, so no list/edit components are declared; the custom
 * dataProvider maps each resource to its hub endpoint via `meta`.
 */
const resources: ResourceProps[] = [
  { name: 'nodes' },
  { name: 'workspaces' },
  { name: 'tasks' },
  { name: 'sessions' },
  { name: 'messages' },
  { name: 'subtasks' },
]

/**
 * Global Refine provider. Passes the app's singleton QueryClient so Refine and
 * the WebSocket-driven cache writes (`WebSocketProvider`, `dispatchEvent`)
 * share exactly one cache — two caches would break live invalidation.
 *
 * `routerProvider` comes from `@refinedev/react-router` 2.0.4. That version
 * exports ONLY the provider plus route-adjacent helpers (CatchAllNavigate,
 * NavigateToResource, DocumentTitleHandler, UnsavedChangesNotifier,
 * useDocumentTitle) — it does NOT re-export `<Routes>`/`<Route>`/`useNavigate`.
 * For React Router 7 the route elements stay the framework's own (see
 * `App.tsx`); this provider is what makes Refine's `useNavigate`/`useBack`/
 * `useParsed` work inside `<Refine>`. `<Refine>` must sit inside the router
 * (it is: `main.tsx` renders `<BrowserRouter>` above `<App />`).
 */
export function RefineProvider({ children }: { children: ReactNode }) {
  return (
    <Refine
      routerProvider={routerProvider}
      dataProvider={dataProvider}
      resources={resources}
      options={{
        reactQuery: { clientConfig: queryClient },
        disableTelemetry: true,
      }}
    >
      {children}
    </Refine>
  )
}
