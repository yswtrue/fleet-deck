import { useCallback, useContext, useSyncExternalStore } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { Breadcrumb, Layout } from 'antd'
import type { BreadcrumbProps } from 'antd'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { WebSocketContext } from '../hooks/WebSocketContext.ts'
import { useHubApi } from '../hooks/useHubApi.ts'
import { UserMenu } from './UserMenu.tsx'
import type { Node as HubNode, Workspace as HubWorkspace } from '../types.ts'
import styles from './Header.module.css'

type Crumb = NonNullable<BreadcrumbProps['items']>[number]

const shortId = (id: string): string => id.slice(0, 8)

/**
 * A cache read for node names, reactive over the query cache so the breadcrumb
 * title appears as soon as the node resolves. Sources: NodeDetail's
 * `['nodes', id]` single-node query and any `['nodes']` list (the WS hello
 * frame seeds one on connect), matching how useCachedWorkspace resolves.
 */
function useCachedNode(nodeId: string): HubNode | undefined {
  const queryClient = useQueryClient()
  const snapshot = useCallback(() => {
    if (!nodeId) return undefined
    for (const [, data] of queryClient.getQueriesData<HubNode | HubNode[]>({ queryKey: ['nodes'] })) {
      if (Array.isArray(data)) {
        const match = data.find((node) => node.id === nodeId)
        if (match) return match
      } else if (data?.id === nodeId) {
        return data
      }
    }
    return undefined
  }, [queryClient, nodeId])
  return useSyncExternalStore(
    (onChange) => queryClient.getQueryCache().subscribe(onChange),
    snapshot,
    snapshot
  )
}

/**
 * Same idea for workspaces: the workspace page's own `['workspace', id]`
 * snapshot is preferred, and every `['workspaces', ...]` list (loaded by
 * WorkspaceList on the node page) is scanned for the matching id, so the
 * breadcrumb can show "Home / node / workspace" without an extra fetch.
 */
function useCachedWorkspace(workspaceId: string): HubWorkspace | undefined {
  const queryClient = useQueryClient()
  const snapshot = useCallback(() => {
    if (!workspaceId) return undefined
    const single = queryClient.getQueryData<HubWorkspace>(['workspace', workspaceId])
    if (single) return single
    for (const [, data] of queryClient.getQueriesData<HubWorkspace[]>({ queryKey: ['workspaces'] })) {
      const match = data?.find((workspace) => workspace.id === workspaceId)
      if (match) return match
    }
    return undefined
  }, [queryClient, workspaceId])
  return useSyncExternalStore(
    (onChange) => queryClient.getQueryCache().subscribe(onChange),
    snapshot,
    snapshot
  )
}

function LogoMark() {
  return (
    <svg className={styles.mark} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <rect x="1.5" y="1.5" width="21" height="21" rx="6" fill="var(--accent-bg)" stroke="var(--accent-border)" />
      <path d="M8.6 11.2 15 7.8M8.6 12.8 15 16.2" stroke="var(--accent)" strokeWidth="1.4" strokeLinecap="round" />
      <circle cx="7" cy="12" r="2.1" fill="var(--accent)" />
      <circle cx="16.4" cy="7.4" r="2.1" fill="var(--accent)" />
      <circle cx="16.4" cy="16.6" r="2.1" fill="var(--accent)" />
    </svg>
  )
}

/**
 * Sticky app chrome: wordmark, route breadcrumb (Home -> node -> workspace,
 * names resolved from the shared query cache), hub + live-socket indicator,
 * and the user menu in the `headerActions` slot (renders null when signed out).
 */
export function Header() {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const location = useLocation()
  const ws = useContext(WebSocketContext)
  const parts = location.pathname.split('/').filter(Boolean)
  const [section, routeId] = parts

  const { data: health } = useQuery({
    queryKey: ['health'],
    queryFn: () => api.getHealth().then(() => true),
    refetchInterval: 5000,
    retry: false,
  })

  const workspaceId = section === 'workspaces' && routeId ? routeId : ''
  const workspace = useCachedWorkspace(workspaceId)
  const nodeId = (section === 'nodes' && routeId ? routeId : '') || workspace?.node_id || ''
  const node = useCachedNode(nodeId)

  const refresh = () => {
    queryClient.invalidateQueries()
  }

  const crumbs: Crumb[] = []
  if (section === 'nodes' && routeId) {
    crumbs.push({
      key: 'node',
      title: <NavLink to={`/nodes/${routeId}`}>{node?.name ?? shortId(routeId)}</NavLink>,
    })
  } else if (section === 'workspaces' && routeId) {
    if (workspace) {
      crumbs.push({
        key: 'node',
        title: <NavLink to={`/nodes/${workspace.node_id}`}>{node?.name ?? shortId(workspace.node_id)}</NavLink>,
      })
    }
    crumbs.push({
      key: 'workspace',
      title: <NavLink to={`/workspaces/${routeId}`}>{workspace?.name ?? shortId(routeId)}</NavLink>,
    })
  } else if (section === 'sessions' && routeId) {
    crumbs.push({
      key: 'session',
      title: <NavLink to={`/sessions/${routeId}`}>Session {shortId(routeId)}</NavLink>,
    })
  }

  const wsLabel =
    ws == null
      ? ''
      : ws.connectionState === 'open'
        ? 'Live'
        : ws.connectionState === 'connecting'
          ? 'Connecting'
          : 'Offline'
  const wsState = ws == null ? 'down' : ws.connected ? 'ok' : ws.connectionState === 'connecting' ? 'pending' : 'down'

  return (
    <Layout.Header className={styles.header}>
      <div className={styles.left}>
        <NavLink to="/" className={styles.brand} aria-label="FleetDeck home">
          <LogoMark />
          <span className={styles.wordmark}>FleetDeck</span>
        </NavLink>
        <Breadcrumb
          className={styles.breadcrumb}
          items={[{ key: 'home', title: <NavLink to="/">Home</NavLink> }, ...crumbs]}
        />
      </div>
      <div className={styles.right}>
        <button
          type="button"
          className={styles.health}
          onClick={refresh}
          aria-label="Refresh hub and live connection status"
        >
          <span className={styles.dot} data-state={health === true ? 'ok' : 'down'} />
          <span className={styles.label}>{health === true ? 'Hub online' : 'Hub offline'}</span>
          {ws != null && (
            <>
              <span className={styles.divider} />
              <span className={styles.dot} data-state={wsState} />
              <span className={styles.label}>{wsLabel}</span>
            </>
          )}
        </button>
        {/* User menu (auth slice); renders null while signed out. */}
        <div id="headerActions" className={styles.headerActions}>
          <UserMenu />
        </div>
      </div>
    </Layout.Header>
  )
}
