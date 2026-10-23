import { useCallback, useContext, useSyncExternalStore } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { Breadcrumb, Layout } from 'antd'
import type { BreadcrumbProps } from 'antd'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { WebSocketContext } from '../hooks/WebSocketContext.ts'
import { useHubApi } from '../hooks/useHubApi.ts'
import { UserMenu } from './UserMenu.tsx'
import { LanguageSwitcher } from './LanguageSwitcher.tsx'
import { InstallAppButton } from './InstallAppButton.tsx'
import { useTranslation } from '../i18n/index.ts'
import type { Node as HubNode, Workspace as HubWorkspace } from '../types.ts'
import styles from './Header.module.css'
import logoUrl from '../assets/fleetdeck-icon.png?w=64&format=webp'
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
    for (const [, data] of queryClient.getQueriesData<HubNode | HubNode[]>({
      queryKey: ['nodes'],
    })) {
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
    for (const [, data] of queryClient.getQueriesData<HubWorkspace[]>({
      queryKey: ['workspaces'],
    })) {
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
  return <img className={styles.mark} src={logoUrl} alt="" />
}

/**
 * Sticky app chrome: wordmark, route breadcrumb (Home -> node -> workspace,
 * names resolved from the shared query cache), hub + live-socket indicator,
 * and the user menu in the `headerActions` slot (renders null when signed out).
 * `onMenuClick` renders a hamburger that opens the small-screen nav drawer
 * (AppLayout passes it only below the sidebar breakpoint).
 */
export function Header({ onMenuClick }: { onMenuClick?: () => void }) {
  const { t } = useTranslation('nav')
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
        title: (
          <NavLink to={`/nodes/${workspace.node_id}`}>
            {node?.name ?? shortId(workspace.node_id)}
          </NavLink>
        ),
      })
    }
    crumbs.push({
      key: 'workspace',
      title: <NavLink to={`/workspaces/${routeId}`}>{workspace?.name ?? shortId(routeId)}</NavLink>,
    })
  } else if (section === 'sessions' && routeId) {
    crumbs.push({
      key: 'session',
      title: (
        <NavLink to={`/sessions/${routeId}`}>{t('sessionCrumb', { id: shortId(routeId) })}</NavLink>
      ),
    })
  }

  const wsLabel =
    ws == null
      ? ''
      : ws.connectionState === 'open'
        ? t('live')
        : ws.connectionState === 'connecting'
          ? t('connecting')
          : t('common:status.offline')
  const wsState =
    ws == null
      ? 'down'
      : ws.connected
        ? 'ok'
        : ws.connectionState === 'connecting'
          ? 'pending'
          : 'down'

  return (
    <Layout.Header className={styles.header}>
      <div className={styles.left}>
        {onMenuClick && (
          <button
            type="button"
            className={styles.menuButton}
            onClick={onMenuClick}
            aria-label={t('openNav')}
          >
            <svg
              viewBox="0 0 24 24"
              aria-hidden="true"
              focusable="false"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinecap="round"
            >
              <path d="M4 7h16M4 12h16M4 17h16" />
            </svg>
          </button>
        )}
        {onMenuClick && (
          <NavLink to="/" className={styles.brand} aria-label={t('brandHome')}>
            <LogoMark />
            <span className={styles.wordmark}>FleetDeck</span>
          </NavLink>
        )}
        <Breadcrumb
          className={styles.breadcrumb}
          items={[{ key: 'home', title: <NavLink to="/">{t('home')}</NavLink> }, ...crumbs]}
        />
      </div>
      <div className={styles.right}>
        <button
          type="button"
          className={styles.health}
          onClick={refresh}
          aria-label={t('refreshStatus')}
        >
          <span className={styles.dot} data-state={health === true ? 'ok' : 'down'} />
          <span className={styles.label}>{health === true ? t('hubOnline') : t('hubOffline')}</span>
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
          <InstallAppButton />
          <LanguageSwitcher />
          <UserMenu />
        </div>
      </div>
    </Layout.Header>
  )
}
