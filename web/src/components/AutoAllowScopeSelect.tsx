import { Select } from 'antd'
import { useTranslation } from '../i18n/index.ts'
import type { Workspace } from '../gen/protocol/index.ts'

/** Every range a workspace can auto-allow ACP permission requests in. */
const SCOPES: Workspace['auto_allow_scope'][] = ['none', 'workspace', 'all']

/**
 * AutoAllowScopeSelect edits the workspace's permission auto-allow range. The
 * labels spell the range out because the setting decides when the permission
 * picker is skipped entirely.
 */
export function AutoAllowScopeSelect({
  value,
  onChange,
  busy = false,
}: {
  value: Workspace['auto_allow_scope']
  onChange: (scope: Workspace['auto_allow_scope']) => void
  busy?: boolean
}) {
  const { t } = useTranslation('board')
  return (
    <Select
      size="small"
      value={value}
      options={SCOPES.map((scope) => ({
        value: scope,
        label: t(`workspace.autoAllowScope.${scope}`),
      }))}
      onChange={onChange}
      loading={busy}
      aria-label={t('workspace.autoAllow')}
    />
  )
}
