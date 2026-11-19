import { useNavigate } from 'react-router-dom'
import { useFavoriteWorkspaces } from '../hooks/useFavoriteWorkspaces.ts'
import { useTranslation } from '../i18n/index.ts'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import styles from './FavoriteWorkspaces.module.css'

/**
 * Dashboard quick-jump strip: the caller's bookmarked workspaces, newest
 * first. Membership comes from the same cache the star toggle writes, so an
 * unfavorite disappears here immediately.
 */
export function FavoriteWorkspaces() {
  const { t } = useTranslation('pages')
  const navigate = useNavigate()
  const favoritesQuery = useFavoriteWorkspaces()
  const favorites = favoritesQuery.data ?? []

  return (
    <section className="fd-stack">
      <h2 className="fd-section-title">{t('dashboard.favorites.title')}</h2>
      {favoritesQuery.isLoading ? (
        <LoadingSkeleton count={1} />
      ) : favorites.length === 0 ? (
        <p className={styles.empty}>{t('dashboard.favorites.empty')}</p>
      ) : (
        <div className={styles.grid}>
          {favorites.map((workspace) => (
            <button
              key={workspace.id}
              type="button"
              className={styles.item}
              aria-label={t('workspace.cardAria', { name: workspace.name })}
              onClick={() => navigate(`/workspaces/${workspace.id}`)}
            >
              <span className={styles.name} title={workspace.name}>
                {workspace.name}
              </span>
              <span className={styles.path} title={workspace.path}>
                {workspace.path}
              </span>
            </button>
          ))}
        </div>
      )}
    </section>
  )
}
