import { useState, useSyncExternalStore } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { Drawer, Layout, Menu } from 'antd'
import type { MenuProps } from 'antd'
import { useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth.ts'
import { Header } from './Header.tsx'
import { useTranslation } from '../i18n/index.ts'
import styles from './AppLayout.module.css'

const { Sider } = Layout

interface IconProps {
  /** antd clones menu icons and injects its own class/style — forward both. */
  className?: string
  style?: CSSProperties
}

/**
 * Inline section icons for the sidebar menu (stroke = currentColor). `antd`
 * adds `ant-menu-item-icon` via cloneElement, which powers the collapsed
 * icon-only rendering — the class must reach the `<svg>`.
 */
function MenuSvg({ className, style, children }: IconProps & { children: ReactNode }) {
  return (
    <svg
      className={[styles.menuIcon, className].filter(Boolean).join(' ')}
      style={style}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  )
}

const DashboardIcon = (props: IconProps) => (
  <MenuSvg {...props}>
    <rect x="3.5" y="3.5" width="7" height="7" rx="1.5" />
    <rect x="13.5" y="3.5" width="7" height="7" rx="1.5" />
    <rect x="3.5" y="13.5" width="7" height="7" rx="1.5" />
    <rect x="13.5" y="13.5" width="7" height="7" rx="1.5" />
  </MenuSvg>
)

const NodesIcon = (props: IconProps) => (
  <MenuSvg {...props}>
    <rect x="3.5" y="4" width="17" height="6" rx="2" />
    <rect x="3.5" y="14" width="17" height="6" rx="2" />
    <path d="M7 7h.01M7 17h.01" />
  </MenuSvg>
)

const UsageIcon = (props: IconProps) => (
  <MenuSvg {...props}>
    <path d="M5 20V12M12 20V5M19 20v-5" />
    <path d="M3 20h18" />
  </MenuSvg>
)

const UsersIcon = (props: IconProps) => (
  <MenuSvg {...props}>
    <circle cx="9" cy="8" r="3.2" />
    <path d="M3.5 19.5c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
    <circle cx="17" cy="9" r="2.5" />
    <path d="M17 14.5c2.2 0 4 1.6 4 4" />
  </MenuSvg>
)

const FoldIcon = () => (
  <MenuSvg>
    <path d="M15 5l-6 7 6 7" />
  </MenuSvg>
)

const UnfoldIcon = () => (
  <MenuSvg>
    <path d="M9 5l6 7-6 7" />
  </MenuSvg>
)

/** Reactive `matchMedia` read for the small-screen (drawer menu) layout. */
function useIsSmallScreen(): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const query = window.matchMedia('(max-width: 768px)')
      query.addEventListener('change', onChange)
      return () => query.removeEventListener('change', onChange)
    },
    () => window.matchMedia('(max-width: 768px)').matches,
    () => false
  )
}

/**
 * Sidebar selection follows the route section: nodes, workspaces and sessions
 * all live under the "nodes" section.
 */
function selectedKey(pathname: string): string {
  if (pathname === '/') return '/'
  if (pathname.startsWith('/usage')) return '/usage'
  if (pathname.startsWith('/users')) return '/users'
  return '/nodes'
}

/**
 * The traditional admin shell: a collapsible left sidebar (a drawer below
 * 768px) plus the existing top header and the routed page content. The menu is
 * the primary navigation; deep links keep working without JS menu state.
 */
export function AppLayout({ children }: { children: ReactNode }) {
  const { t } = useTranslation('nav')
  const { isAdmin } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const isSmall = useIsSmallScreen()
  const [collapsed, setCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const items: MenuProps['items'] = [
    { key: '/', icon: <DashboardIcon />, label: t('dashboard') },
    { key: '/nodes', icon: <NodesIcon />, label: t('nodes') },
    { key: '/usage', icon: <UsageIcon />, label: t('usage') },
    ...(isAdmin ? [{ key: '/users', icon: <UsersIcon />, label: t('users') }] : []),
  ]

  const menu = (
    <Menu
      mode="inline"
      selectedKeys={[selectedKey(location.pathname)]}
      items={items}
      onClick={({ key }) => {
        setDrawerOpen(false)
        navigate(key)
      }}
    />
  )

  return (
    <Layout className={styles.layout}>
      {!isSmall && (
        <Sider
          className={styles.sider}
          theme="light"
          width={220}
          collapsedWidth={64}
          collapsible
          collapsed={collapsed}
          onCollapse={setCollapsed}
          trigger={null}
        >
          <div className={styles.siderTop}>
            <button
              type="button"
              className={styles.collapse}
              onClick={() => setCollapsed((value) => !value)}
              aria-label={collapsed ? t('expandNav') : t('collapseNav')}
            >
              {collapsed ? <UnfoldIcon /> : <FoldIcon />}
            </button>
          </div>
          {menu}
        </Sider>
      )}
      <Layout className={styles.main}>
        <Header onMenuClick={isSmall ? () => setDrawerOpen(true) : undefined} />
        <Layout.Content className="appMain">{children}</Layout.Content>
      </Layout>
      <Drawer
        placement="left"
        width={260}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        styles={{ body: { padding: 0 } }}
      >
        {menu}
      </Drawer>
    </Layout>
  )
}
